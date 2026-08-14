package ipcam

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

// ErrLoopbackUnavailable is returned when the running build has no
// v4l2loopback module. Its text is user-facing: EnsureNodes swallows it into
// a one-time log so the rest of the agent degrades gracefully, but anything
// that surfaces it to a person (a CLI command, a status field) should show
// this text as-is rather than paraphrasing it.
var ErrLoopbackUnavailable = errors.New("running WendyOS build lacks the v4l2loopback module; `camera view` still works")

// PumpFunc starts the in-process GStreamer helper that copies an RTSP
// camera's stream into its v4l2loopback node. Per branch-C-preamble.md, this
// is `wendy-agent utils ipcam-gstreamer` invoked through the purego helper
// (services/ipcam_gstreamer_process.go), not a shelled-out gst-launch-1.0, so
// camera credentials never touch argv.
//
// Task C2 only carries this through the constructor so its signature does not
// have to change later; Task C3 wires the pump and refcounting on top of it.
type PumpFunc func(ctx context.Context, args []string) error

// loopbackDeps seams every syscall and subprocess the loopback node manager
// touches, so the whole package builds and its tests run on macOS. Linux's
// real implementations live in loopback_linux.go; the non-Linux stubs that
// make Available degrade cleanly live in loopback_other.go.
type loopbackDeps struct {
	statControl func() error // stat /dev/v4l2loopback

	// modprobe loads the module. The ABI's "try devices=0/exclusive_caps=1,
	// retry a plain load on param rejection" policy is internal to a single
	// call here, so callers (and their tests) only ever see one attempt.
	modprobe func(ctx context.Context) error

	addNode    func(nr int, label string) error
	removeNode func(nr int) error
	nodeExists func(nr int) bool // stat /dev/video<nr>
}

// Loopback manages the v4l2loopback nodes that back container-visible camera
// streams: one output device per registered network camera, numbered by the
// camera's own ID so the node a user learns about never changes.
//
// It degrades rather than fails: a WendyOS build without the kernel module
// (or a macOS dev build, which never has it) still serves `camera view`
// directly from the camera — only container mirroring is unavailable, and
// Available reports that with ErrLoopbackUnavailable instead of panicking or
// wedging the agent.
type Loopback struct {
	ctx    context.Context
	logger *zap.Logger
	reg    *Registry
	creds  *CredentialStore
	pump   PumpFunc // carried for Task C3; inert until the pump/refcount half lands

	deps loopbackDeps

	detectOnce sync.Once
	detectErr  error
}

// NewLoopback returns a node manager. Detection of the v4l2loopback module is
// deferred to the first call that needs it (Available, EnsureNodes, ...): a
// device without the module still constructs a Loopback cleanly, since the
// whole point of this package is that the module's absence is not a startup
// failure.
func NewLoopback(ctx context.Context, logger *zap.Logger, reg *Registry, creds *CredentialStore, pump PumpFunc) *Loopback {
	return &Loopback{
		ctx:    ctx,
		logger: logger,
		reg:    reg,
		creds:  creds,
		pump:   pump,
		deps:   defaultLoopbackDeps(),
	}
}

// Available reports whether the v4l2loopback module is usable, attempting to
// load it — once, ever, for the life of this Loopback — if it is not already
// present. A non-nil error always wraps ErrLoopbackUnavailable.
func (l *Loopback) Available() error {
	l.detectOnce.Do(func() {
		l.detectErr = l.detect()
	})
	return l.detectErr
}

// detect runs the module-detection policy exactly once, guarded by
// detectOnce: stat the control device; if absent, try to load the module;
// re-stat; if it is still absent, degrade for good and log that once. See
// loopback_linux.go's modprobe implementation for the params-then-plain
// fallback retry — from here it is a single seam call, so a fake in tests can
// prove modprobe is attempted at most once regardless of how many times
// Available is called.
func (l *Loopback) detect() error {
	if err := l.deps.statControl(); err == nil {
		return nil
	}

	if err := l.deps.modprobe(l.ctx); err != nil {
		l.warnUnavailable(err)
		return fmt.Errorf("%w (modprobe: %v)", ErrLoopbackUnavailable, err)
	}
	if err := l.deps.statControl(); err != nil {
		l.warnUnavailable(err)
		return fmt.Errorf("%w (control device still absent after modprobe: %v)", ErrLoopbackUnavailable, err)
	}

	// A plain-fallback load (see loopback_linux.go) does not honor devices=0, so
	// it can auto-create loopback nodes at the kernel's own low numbers before
	// our reserved camera-ID band. Those never belong to a camera; sweep them so
	// they can never collide with — or be mistaken for — one. A devices=0 load
	// creates nothing, so this is a no-op then.
	l.sweepAutoCreatedNodes()
	return nil
}

// sweepAutoCreatedNodes removes any loopback device numbered below the
// reserved camera-ID band (see registry.go's IDBandStart).
func (l *Loopback) sweepAutoCreatedNodes() {
	for nr := 0; nr < IDBandStart; nr++ {
		if !l.deps.nodeExists(nr) {
			continue
		}
		if err := l.deps.removeNode(nr); err != nil {
			l.logger.Warn("removing auto-created v4l2loopback node below the reserved camera-ID band",
				zap.Int("nr", nr), zap.Error(err))
		}
	}
}

// warnUnavailable logs the degradation. It is only ever called from inside
// detectOnce, so — regardless of how many times Available or EnsureNodes are
// called afterward — it fires exactly once for the life of this Loopback.
func (l *Loopback) warnUnavailable(cause error) {
	l.logger.Warn("v4l2loopback module unavailable; camera view still works, but container mirroring is disabled",
		zap.Error(cause))
}

// EnsureNodes creates a v4l2loopback node for every registered camera that
// does not already have one. It is idempotent — nodes that already exist are
// left alone — and it never fails the caller over a missing module: if the
// module is unavailable it returns nil, having already logged that once via
// Available.
func (l *Loopback) EnsureNodes(ctx context.Context) error {
	if err := l.Available(); err != nil {
		return nil
	}
	for _, cam := range l.reg.List() {
		if err := ctx.Err(); err != nil {
			return err
		}
		nr := int(cam.ID)
		if l.deps.nodeExists(nr) {
			continue
		}
		label := fmt.Sprintf("Wendy IP camera %d", cam.ID)
		if err := l.deps.addNode(nr, label); err != nil {
			l.logger.Warn("creating v4l2loopback node", zap.Uint32("cameraId", cam.ID), zap.Error(err))
			continue
		}
	}
	return nil
}

// NodePath returns the loopback device path for a camera and whether it
// currently exists. It always reflects live state, never a cache, so a node
// removed out from under the agent — or never created because the module is
// unavailable — correctly reports false.
func (l *Loopback) NodePath(camID uint32) (string, bool) {
	nr := int(camID)
	if !l.deps.nodeExists(nr) {
		return "", false
	}
	return fmt.Sprintf("/dev/video%d", nr), true
}

// RemoveCamera deletes a camera's v4l2loopback node, if the module is
// available. It is best-effort and has no error to report: a camera being
// forgotten should not fail because its node was already gone, and the
// underlying removeNode already treats "no such node" as success.
func (l *Loopback) RemoveCamera(camID uint32) {
	if err := l.Available(); err != nil {
		return
	}
	nr := int(camID)
	if err := l.deps.removeNode(nr); err != nil {
		l.logger.Warn("removing v4l2loopback node", zap.Uint32("cameraId", camID), zap.Error(err))
	}
}
