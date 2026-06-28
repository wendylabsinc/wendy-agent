package crashreport

import (
	"context"
	"net"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type fakeDiag struct {
	cloudpb.UnimplementedDiagnosticsServiceServer
	failSubmit bool
}

func (f *fakeDiag) SubmitReport(ctx context.Context, req *cloudpb.SubmitReportRequest) (*cloudpb.SubmitReportResponse, error) {
	if f.failSubmit {
		return nil, context.DeadlineExceeded
	}
	return &cloudpb.SubmitReportResponse{TrackingId: "WDY-7Q4ZK2", StatusUrl: "https://wendy.sh/r/WDY-7Q4ZK2"}, nil
}
func (f *fakeDiag) Subscribe(ctx context.Context, req *cloudpb.SubscribeRequest) (*cloudpb.SubscribeResponse, error) {
	return &cloudpb.SubscribeResponse{SubscriptionId: "sub-1"}, nil
}

func dialFake(t *testing.T, srv *fakeDiag) cloudpb.DiagnosticsServiceClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	cloudpb.RegisterDiagnosticsServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return cloudpb.NewDiagnosticsServiceClient(conn)
}

func sampleBundle() Bundle {
	return Build(platforminfo.Info{CLIVersion: "0.1"}, "other", "unrecoverable", "boom", nil, nil)
}

func TestSubmitSuccess(t *testing.T) {
	client := dialFake(t, &fakeDiag{})
	res, err := Submit(context.Background(), client, sampleBundle())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.TrackingID != "WDY-7Q4ZK2" || res.StatusURL == "" {
		t.Errorf("bad result: %+v", res)
	}
}

func TestSubmitFallbackToFileOnError(t *testing.T) {
	client := dialFake(t, &fakeDiag{failSubmit: true})
	res, err := Submit(context.Background(), client, sampleBundle())
	if err != nil {
		t.Fatalf("Submit must not return an error on fallback: %v", err)
	}
	if res.TrackingID != "" || res.LocalFile == "" {
		t.Errorf("expected file fallback, got %+v", res)
	}
}

func TestSubmitNilClientFallsBackToFile(t *testing.T) {
	res, err := Submit(context.Background(), nil, sampleBundle())
	if err != nil || res.LocalFile == "" {
		t.Errorf("nil client should fall back to file: %+v err=%v", res, err)
	}
}
