package services

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
)

// fakeReportedCloud answers every report with no desired state at all (the
// device's apps are unassigned) and a postbox nudge.
type fakeReportedCloud struct {
	cloudpbv2.UnimplementedDeploymentServiceServer
	reports chan *cloudpbv2.UpdateReportedStateRequest
}

func (f *fakeReportedCloud) HandleReportedState(stream grpc.BidiStreamingServer[cloudpbv2.UpdateReportedStateRequest, cloudpbv2.DesiredStateResponse]) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		f.reports <- req
		if err := stream.Send(&cloudpbv2.DesiredStateResponse{PostboxHeadEntryId: proto.Int64(7)}); err != nil {
			return err
		}
	}
}

type fakeAppReporter struct {
	mu   sync.Mutex
	apps []AppReport
}

func (f *fakeAppReporter) ReportApps(context.Context) ([]AppReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AppReport(nil), f.apps...), nil
}

func (f *fakeAppReporter) set(apps ...AppReport) {
	f.mu.Lock()
	f.apps = apps
	f.mu.Unlock()
}

type fakeMetricsPublisher struct{ metrics chan *colmetricspb.ExportMetricsServiceRequest }

func (fakeMetricsPublisher) PublishLogs(*collogspb.ExportLogsServiceRequest)    {}
func (fakeMetricsPublisher) PublishTraces(*coltracepb.ExportTraceServiceRequest) {}
func (f fakeMetricsPublisher) PublishMetrics(req *colmetricspb.ExportMetricsServiceRequest) {
	f.metrics <- req
}

func startReportedState(t *testing.T, apps AppReporter, poll, heartbeat time.Duration) (*fakeReportedCloud, fakeMetricsPublisher) {
	t.Helper()
	cloud := &fakeReportedCloud{reports: make(chan *cloudpbv2.UpdateReportedStateRequest, 16)}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	cloudpbv2.RegisterDeploymentServiceServer(srv, cloud)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	pub := fakeMetricsPublisher{metrics: make(chan *colmetricspb.ExportMetricsServiceRequest, 16)}
	r := NewReportedState(zap.NewNop(), apps, pub, "cloud", "cert", "key", "")
	r.poll, r.heartbeat = poll, heartbeat
	r.dial = func(string, string, string, string) (grpc.ClientConnInterface, io.Closer, error) {
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		return conn, conn, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.Run(ctx)
	return cloud, pub
}

func nextReport(t *testing.T, reports <-chan *cloudpbv2.UpdateReportedStateRequest) *cloudpbv2.UpdateReportedStateRequest {
	t.Helper()
	select {
	case req := <-reports:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("no report reached cloud")
		return nil
	}
}

// An app cloud holds no desired state for is reported all the same, with its
// release identity, and the nudge in the answer is surfaced as a metric.
func TestReportedStateReportsUnassignedApp(t *testing.T) {
	apps := &fakeAppReporter{}
	apps.set(AppReport{AppID: "com.example.cam", ReleaseID: "0b6f2c1e-8a3d-4c5e-9f00-1234567890ab", ManifestDigest: "sha256:abc", Running: true})
	cloud, pub := startReportedState(t, apps, time.Hour, time.Hour)

	want := &cloudpbv2.UpdateReportedStateRequest{CurrentStates: []*cloudpbv2.CurrentState{{
		AppId:          "com.example.cam",
		AppReleaseId:   proto.String("0b6f2c1e-8a3d-4c5e-9f00-1234567890ab"),
		ReportedState:  cloudpbv2.Running_RUNNING,
		ManifestDigest: "sha256:abc",
	}}}
	if got := nextReport(t, cloud.reports); !proto.Equal(got, want) {
		t.Fatalf("report = %v, want %v", got, want)
	}
	select {
	case m := <-pub.metrics:
		dp := m.GetResourceMetrics()[0].GetScopeMetrics()[0].GetMetrics()[0].GetGauge().GetDataPoints()[0]
		if dp.GetAsInt() != 7 {
			t.Fatalf("postbox head metric = %d, want 7", dp.GetAsInt())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("postbox nudge not surfaced as a metric")
	}
}

// An unchanged state is resent on the heartbeat; a change is sent at once.
func TestReportedStateHeartbeatAndChange(t *testing.T) {
	apps := &fakeAppReporter{}
	apps.set(AppReport{AppID: "com.example.cam", Running: true})
	cloud, _ := startReportedState(t, apps, 10*time.Millisecond, 100*time.Millisecond)

	first := nextReport(t, cloud.reports)
	if again := nextReport(t, cloud.reports); !proto.Equal(again, first) {
		t.Fatalf("heartbeat report = %v, want the unchanged %v", again, first)
	}
	apps.set(AppReport{AppID: "com.example.cam", ExitSignal: 9})
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		got := nextReport(t, cloud.reports)
		if s := got.GetCurrentStates()[0]; s.GetReportedState() == cloudpbv2.Running_STOPPED && s.GetReportedLastExitSignal() == 9 {
			return
		}
	}
	t.Fatal("changed state never reported")
}

// Without a device identity (not enrolled) nothing is dialed.
func TestReportedStateSilentWhenNotEnrolled(t *testing.T) {
	r := NewReportedState(zap.NewNop(), &fakeAppReporter{}, nil, "", "", "", "")
	r.dial = func(string, string, string, string) (grpc.ClientConnInterface, io.Closer, error) {
		t.Fatal("dialed cloud without an identity")
		return nil, nil, nil
	}
	if err := r.session(context.Background()); err != nil {
		t.Fatalf("session = %v, want nil", err)
	}
}
