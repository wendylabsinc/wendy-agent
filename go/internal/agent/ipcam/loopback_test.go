package ipcam

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// loopbackHarness wires a Loopback whose every seam is captured in memory, so
// module detection and node lifecycle are exercised with no real device and
// no ioctl — everything here runs on macOS as well as Linux.
type loopbackHarness struct {
	mu sync.Mutex

	controlExists bool
	modprobeCalls int
	modprobeErr   error

	nodes        map[int]bool // nr -> exists
	addNodeCalls []addNodeCall
	addNodeErr   error
	removeCalls  []int
}

type addNodeCall struct {
	nr    int
	label string
}

func newLoopbackHarness() *loopbackHarness {
	return &loopbackHarness{nodes: make(map[int]bool)}
}

func (h *loopbackHarness) deps() loopbackDeps {
	return loopbackDeps{
		statControl: func() error {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.controlExists {
				return nil
			}
			return os.ErrNotExist
		},
		modprobe: func(ctx context.Context) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.modprobeCalls++
			if h.modprobeErr == nil {
				h.controlExists = true
			}
			return h.modprobeErr
		},
		addNode: func(nr int, label string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.addNodeErr != nil {
				return h.addNodeErr
			}
			h.addNodeCalls = append(h.addNodeCalls, addNodeCall{nr: nr, label: label})
			h.nodes[nr] = true
			return nil
		},
		removeNode: func(nr int) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.removeCalls = append(h.removeCalls, nr)
			delete(h.nodes, nr)
			return nil
		},
		nodeExists: func(nr int) bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.nodes[nr]
		},
	}
}

func (h *loopbackHarness) modprobeCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.modprobeCalls
}

func (h *loopbackHarness) addNodeCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.addNodeCalls)
}

func (h *loopbackHarness) setNodeExists(nr int, exists bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nodes[nr] = exists
}

func newTestLoopback(t *testing.T, h *loopbackHarness) (*Loopback, *Registry) {
	t.Helper()
	reg := NewRegistry(filepath.Join(t.TempDir(), "cameras.json"))
	if err := reg.Load(); err != nil {
		t.Fatalf("registry load: %v", err)
	}
	creds := NewCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := creds.Load(); err != nil {
		t.Fatalf("credentials load: %v", err)
	}
	l := NewLoopback(context.Background(), zap.NewNop(), reg, creds, nil)
	l.deps = h.deps()
	return l, reg
}

// A control device that already exists means the module is already loaded:
// Available must report that without ever touching modprobe.
func TestLoopback_AvailableWhenControlDeviceExists(t *testing.T) {
	h := newLoopbackHarness()
	h.controlExists = true
	l, _ := newTestLoopback(t, h)

	if err := l.Available(); err != nil {
		t.Fatalf("Available() = %v, want nil", err)
	}
	if calls := h.modprobeCallCount(); calls != 0 {
		t.Fatalf("modprobe called %d times, want 0 when the control device already exists", calls)
	}
}

// When the module can't be loaded at all, Available must degrade to a wrapped
// ErrLoopbackUnavailable — and detection, including the modprobe attempt,
// must run at most once no matter how many times Available is called.
func TestLoopback_AvailableAttemptsModprobeOnceThenDegrades(t *testing.T) {
	h := newLoopbackHarness()
	h.modprobeErr = errors.New("modprobe: FATAL: Module v4l2loopback not found")
	l, _ := newTestLoopback(t, h)

	err1 := l.Available()
	err2 := l.Available()

	if !errors.Is(err1, ErrLoopbackUnavailable) {
		t.Fatalf("Available() #1 = %v, want an error wrapping ErrLoopbackUnavailable", err1)
	}
	if !errors.Is(err2, ErrLoopbackUnavailable) {
		t.Fatalf("Available() #2 = %v, want an error wrapping ErrLoopbackUnavailable", err2)
	}
	if calls := h.modprobeCallCount(); calls != 1 {
		t.Fatalf("modprobe called %d times across two Available() calls, want exactly 1", calls)
	}
}

// EnsureNodes must create a node for every registered camera that lacks one,
// and calling it again must not recreate nodes that already exist.
func TestLoopback_EnsureNodesCreatesMissingAndIsIdempotent(t *testing.T) {
	h := newLoopbackHarness()
	h.controlExists = true
	l, reg := newTestLoopback(t, h)

	cam1, err := reg.Upsert(Camera{MAC: "ec:71:db:2a:ae:7e", Address: "10.98.0.10"})
	if err != nil {
		t.Fatalf("upsert cam1: %v", err)
	}
	cam2, err := reg.Upsert(Camera{MAC: "ec:71:db:2a:ae:7f", Address: "10.98.0.11"})
	if err != nil {
		t.Fatalf("upsert cam2: %v", err)
	}

	if err := l.EnsureNodes(context.Background()); err != nil {
		t.Fatalf("EnsureNodes() #1 = %v", err)
	}
	if calls := h.addNodeCallCount(); calls != 2 {
		t.Fatalf("addNode called %d times, want 2 (one per registered camera)", calls)
	}
	wantNrs := map[int]bool{int(cam1.ID): true, int(cam2.ID): true}
	h.mu.Lock()
	for _, c := range h.addNodeCalls {
		if !wantNrs[c.nr] {
			t.Errorf("addNode called with nr=%d, which is not a registered camera ID", c.nr)
		}
	}
	h.mu.Unlock()

	if err := l.EnsureNodes(context.Background()); err != nil {
		t.Fatalf("EnsureNodes() #2 = %v", err)
	}
	if calls := h.addNodeCallCount(); calls != 2 {
		t.Fatalf("addNode called %d times total after a second EnsureNodes, want still 2 (idempotent)", calls)
	}
}

// Without the module, EnsureNodes must not fail the caller: it degrades to a
// nil error (the degradation itself was already logged once by Available),
// and it must not attempt to create any nodes.
func TestLoopback_EnsureNodesDegradesToNilErrorWithoutModule(t *testing.T) {
	h := newLoopbackHarness()
	h.modprobeErr = errors.New("modprobe: FATAL: Module v4l2loopback not found")
	l, reg := newTestLoopback(t, h)
	if _, err := reg.Upsert(Camera{MAC: "ec:71:db:2a:ae:7e", Address: "10.98.0.10"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := l.EnsureNodes(context.Background()); err != nil {
		t.Fatalf("EnsureNodes() = %v, want nil even though the module is unavailable", err)
	}
	if calls := h.addNodeCallCount(); calls != 0 {
		t.Fatalf("addNode called %d times, want 0 when the module is unavailable", calls)
	}
}

// NodePath must reflect live existence, not a guess from the registry: a
// camera ID with no node reports false, and one with a node reports its path.
func TestLoopback_NodePathReportsOnlyExistingNodes(t *testing.T) {
	h := newLoopbackHarness()
	h.controlExists = true
	l, _ := newTestLoopback(t, h)

	if path, ok := l.NodePath(203); ok {
		t.Fatalf("NodePath(203) = (%q, true), want false: no node was ever created", path)
	}

	h.setNodeExists(203, true)

	path, ok := l.NodePath(203)
	if !ok {
		t.Fatal("NodePath(203) reported false for a node that exists")
	}
	if path != "/dev/video203" {
		t.Fatalf("NodePath(203) = %q, want /dev/video203", path)
	}
}

// RemoveCamera must issue the REMOVE ioctl for the camera's node and leave
// NodePath reporting it gone afterward.
func TestLoopback_RemoveCameraRemovesNode(t *testing.T) {
	h := newLoopbackHarness()
	h.controlExists = true
	l, reg := newTestLoopback(t, h)
	cam, err := reg.Upsert(Camera{MAC: "ec:71:db:2a:ae:7e", Address: "10.98.0.10"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := l.EnsureNodes(context.Background()); err != nil {
		t.Fatalf("EnsureNodes() = %v", err)
	}
	if _, ok := l.NodePath(cam.ID); !ok {
		t.Fatal("precondition failed: EnsureNodes did not create the camera's node")
	}

	l.RemoveCamera(cam.ID)

	h.mu.Lock()
	removed := append([]int(nil), h.removeCalls...)
	h.mu.Unlock()
	if len(removed) != 1 || removed[0] != int(cam.ID) {
		t.Fatalf("removeNode calls = %v, want exactly [%d]", removed, cam.ID)
	}
	if path, ok := l.NodePath(cam.ID); ok {
		t.Fatalf("NodePath(%d) = (%q, true) after RemoveCamera, want false", cam.ID, path)
	}
}
