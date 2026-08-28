package mcusource_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/mcusource"
	"github.com/wendylabsinc/wendy/go/internal/agent/ros2camera"
	"github.com/wendylabsinc/wendy/go/internal/agent/sensorlink/sim"
	sensorlinkpb "github.com/wendylabsinc/wendy/go/proto/gen/sensorlinkpb"
	"go.uber.org/zap"
)

type fakeLoopback struct {
	mu      sync.Mutex
	ensured []uint32
}

func (f *fakeLoopback) EnsureNode(_ context.Context, id uint32, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, id)
	return nil
}
func (f *fakeLoopback) NodePath(id uint32) (string, bool) { return "/dev/video-fake", true }

type fakeWriter struct {
	mu     sync.Mutex
	frames int
}

func (w *fakeWriter) WriteFrame(ros2camera.Frame) error { w.mu.Lock(); w.frames++; w.mu.Unlock(); return nil }
func (w *fakeWriter) Close() error                      { return nil }
func (w *fakeWriter) count() int                        { w.mu.Lock(); defer w.mu.Unlock(); return w.frames }

func TestSupervisorMountsCameraAndWritesFrames(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sim.Serve(ctx, ln, sim.Options{
		Manifest:      &sensorlinkpb.SensorManifest{DeviceAssetId: 8, Sensors: []*sensorlinkpb.SensorDescriptor{{ChannelId: 1, Kind: sensorlinkpb.SensorDescriptor_CAMERA, Name: "cam0", Format: &sensorlinkpb.SensorDescriptor_Video{Video: &sensorlinkpb.VideoFormat{Codec: sensorlinkpb.VideoFormat_MJPEG, Width: 4, Height: 4}}}}},
		Frames:        [][]byte{[]byte("jpg")},
		FrameInterval: time.Millisecond,
	})

	lb := &fakeLoopback{}
	w := &fakeWriter{}
	sup := mcusource.NewSupervisor(zap.NewNop(), lb, tcpDialer{}, func(string) ros2camera.CameraWriter { return w })

	rctx, rcancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer rcancel()
	_ = sup.RunPairing(rctx, mcusource.SensorPairing{SourceAssetID: 8, OrgID: 1}, ln.Addr().String())

	if len(lb.ensured) == 0 || lb.ensured[0] < 256 {
		t.Fatalf("expected an MCU-band node to be ensured, got %v", lb.ensured)
	}
	if w.count() == 0 {
		t.Fatal("expected frames written to the camera writer")
	}
}
