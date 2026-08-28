package ros2camera

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/rtps"
)

type fakeLoopback struct {
	mu    sync.Mutex
	paths map[uint32]string
}

func (f *fakeLoopback) EnsureNode(_ context.Context, id uint32, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paths == nil {
		f.paths = map[uint32]string{}
	}
	f.paths[id] = fmt.Sprintf("/dev/video%d", id)
	return nil
}
func (f *fakeLoopback) NodePath(id uint32) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.paths[id]
	return p, ok
}

type fakeWriter struct {
	frames int
	width  int
	height int
}

func (w *fakeWriter) WriteJPEG(_ []byte, width, height int) error {
	w.frames++
	w.width, w.height = width, height
	return nil
}
func (*fakeWriter) Close() error { return nil }

func TestManagerRegistersROS2AndGo2CamerasWithStableIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ros2-cameras.json")
	loop := &fakeLoopback{}
	m := NewManager(context.Background(), zap.NewNop(), loop, path, nil)
	t.Cleanup(m.Shutdown)
	p := &participantState{iface: "eth0", domainID: 0}
	m.registerEndpoint(p, rtps.Endpoint{Topic: "rt/frontvideostream", Type: TypeGo2FrontVideo, GUID: rtps.GUID{EntityID: 1}})
	m.registerEndpoint(p, rtps.Endpoint{Topic: "rt/camera/image_raw", Type: TypeImage, GUID: rtps.GUID{EntityID: 2}})

	cameras := m.List()
	if len(cameras) != 2 {
		t.Fatalf("cameras = %+v", cameras)
	}
	if cameras[0].ID != IDBandStart || cameras[0].Name != "Unitree Go2 front camera" || cameras[0].Topic != "/frontvideostream" || cameras[0].Path != "/dev/video128" {
		t.Fatalf("Go2 camera = %+v", cameras[0])
	}

	// A fresh manager reading the registry must retain the topic's device ID.
	m2 := NewManager(context.Background(), zap.NewNop(), &fakeLoopback{}, path, nil)
	t.Cleanup(m2.Shutdown)
	m2.registerEndpoint(p, rtps.Endpoint{Topic: "rt/frontvideostream", Type: TypeGo2FrontVideo, GUID: rtps.GUID{EntityID: 3}})
	got := m2.List()
	if len(got) != 1 || got[0].ID != cameras[0].ID {
		t.Fatalf("reloaded camera = %+v, want stable ID %d", got, cameras[0].ID)
	}
}

func TestManagerPumpsCompressedImageToLoopback(t *testing.T) {
	loop := &fakeLoopback{}
	m := NewManager(context.Background(), zap.NewNop(), loop, filepath.Join(t.TempDir(), "registry.json"), nil)
	t.Cleanup(m.Shutdown)
	writer := &fakeWriter{}
	m.newWriter = func(string) cameraWriter { return writer }
	m.containerUse = true
	guid := rtps.GUID{EntityID: 7}
	m.registerEndpoint(&participantState{iface: "eth0", domainID: 0}, rtps.Endpoint{Topic: "rt/camera/compressed", Type: TypeCompressedImage, GUID: guid})

	jpegFrame := testJPEG(t, 5, 4)
	c := newCDRBuilder()
	c.header()
	c.str("jpeg")
	c.bytes(jpegFrame)
	m.handleSample(rtps.Sample{Writer: guid, Payload: c.b})
	m.handleSample(rtps.Sample{Writer: guid, Payload: c.b})

	if writer.frames != 1 || writer.width != 5 || writer.height != 4 {
		t.Fatalf("writer = %+v", writer)
	}
}

func TestManagerSkipsDecodeWhenLoopbackNodeIsMissing(t *testing.T) {
	loop := &fakeLoopback{}
	m := NewManager(context.Background(), zap.NewNop(), loop, filepath.Join(t.TempDir(), "registry.json"), nil)
	t.Cleanup(m.Shutdown)
	m.containerUse = true
	guid := rtps.GUID{EntityID: 7}
	m.registerEndpoint(&participantState{iface: "eth0", domainID: 0, graphKey: "host:eth0"}, rtps.Endpoint{
		Topic: "rt/camera/compressed", Type: TypeCompressedImage, GUID: guid,
	})
	loop.mu.Lock()
	delete(loop.paths, IDBandStart)
	loop.mu.Unlock()

	m.handleSample(rtps.Sample{Writer: guid, Payload: []byte("not CDR")})
	if m.cameras[IDBandStart].loggedError {
		t.Fatal("missing loopback node should skip decoding without logging a decode error")
	}
}

func TestManagerKeepsHostInterfacesDistinct(t *testing.T) {
	m := NewManager(context.Background(), zap.NewNop(), &fakeLoopback{}, filepath.Join(t.TempDir(), "registry.json"), nil)
	t.Cleanup(m.Shutdown)
	topic := "rt/camera/compressed"
	m.registerEndpoint(&participantState{iface: "eth0", domainID: 0, graphKey: "host:eth0"}, rtps.Endpoint{
		Topic: topic, Type: TypeCompressedImage, GUID: rtps.GUID{EntityID: 1},
	})
	m.registerEndpoint(&participantState{iface: "eth1", domainID: 0, graphKey: "host:eth1"}, rtps.Endpoint{
		Topic: topic, Type: TypeCompressedImage, GUID: rtps.GUID{EntityID: 2},
	})
	if cameras := m.List(); len(cameras) != 2 || cameras[0].ID == cameras[1].ID {
		t.Fatalf("cameras = %+v; want distinct cameras for each host interface", cameras)
	}
}

func TestManagerRejectsUnsafeTopicNames(t *testing.T) {
	m := NewManager(context.Background(), zap.NewNop(), &fakeLoopback{}, filepath.Join(t.TempDir(), "registry.json"), nil)
	t.Cleanup(m.Shutdown)
	m.registerEndpoint(&participantState{iface: "eth0", domainID: 0, graphKey: "host:eth0"}, rtps.Endpoint{
		Topic: "rt/camera\nforged", Type: TypeCompressedImage, GUID: rtps.GUID{EntityID: 1},
	})
	if cameras := m.List(); len(cameras) != 0 {
		t.Fatalf("unsafe topic was registered: %+v", cameras)
	}
}

func TestManagerRediscoveryDoesNotResetSubscription(t *testing.T) {
	m := NewManager(context.Background(), zap.NewNop(), &fakeLoopback{}, filepath.Join(t.TempDir(), "registry.json"), nil)
	t.Cleanup(m.Shutdown)
	p := &participantState{iface: "eth0", domainID: 0}
	first := rtps.GUID{EntityID: 7}
	m.registerEndpoint(p, rtps.Endpoint{Topic: "rt/camera/compressed", Type: TypeCompressedImage, GUID: first})

	cam := m.cameras[IDBandStart]
	cam.subscribed = true
	m.registerEndpoint(p, rtps.Endpoint{Topic: "rt/camera/compressed", Type: TypeCompressedImage, GUID: first})
	if !cam.subscribed {
		t.Fatal("duplicate endpoint announcement reset the active subscription")
	}

	second := rtps.GUID{EntityID: 8}
	m.registerEndpoint(p, rtps.Endpoint{Topic: "rt/camera/compressed", Type: TypeCompressedImage, GUID: second})
	if m.byWriter[first] != nil || m.byWriter[second] != cam {
		t.Fatalf("writer index was not replaced: old=%p new=%p camera=%p", m.byWriter[first], m.byWriter[second], cam)
	}
}
