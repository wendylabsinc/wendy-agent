package oci

import (
	"strings"
	"testing"
)

// withHostSockets makes only the listed paths look like live sockets.
func withHostSockets(t *testing.T, paths ...string) {
	t.Helper()
	present := map[string]bool{}
	for _, p := range paths {
		present[p] = true
	}
	prev := isSocket
	isSocket = func(path string) bool { return present[path] }
	t.Cleanup(func() { isSocket = prev })
}

// withPipeWire points the socket probe at a fake running daemon for one test.
func withPipeWire(t *testing.T, socket string) {
	t.Helper()
	prev := pipewireSocketHostPath
	pipewireSocketHostPath = func() string { return socket }
	t.Cleanup(func() { pipewireSocketHostPath = prev })
}

// withPipeWireGroup stubs the host group lookup for one test.
func withPipeWireGroup(t *testing.T, gid uint32, ok bool) {
	t.Helper()
	prev := lookupGroupGID
	lookupGroupGID = func(string) (uint32, bool) { return gid, ok }
	t.Cleanup(func() { lookupGroupGID = prev })
}

// The camera entitlement has to offer the graph, not just the device nodes:
// /dev/videoN alone gives the container the kernel's single capture slot, so no
// other app or viewer can read the camera while it runs.
func TestApplyCamera_MountsPipeWireSocket(t *testing.T) {
	withPipeWire(t, "/run/pipewire/pipewire-0")
	withPipeWireGroup(t, 997, true)

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	applyCamera(spec)

	if !hasMountDest(spec, "/run/pipewire/pipewire-0") {
		t.Error("camera entitlement did not mount the PipeWire socket")
	}
	if !hasEnv(spec, "PIPEWIRE_RUNTIME_DIR") {
		t.Error("camera entitlement did not set PIPEWIRE_RUNTIME_DIR")
	}
	if !hasGID(spec, 997) {
		t.Error("camera entitlement did not add the pipewire group")
	}
}

// A user-session socket (RPi OS layout) is mounted at the system path so
// PIPEWIRE_RUNTIME_DIR is the same inside every container.
func TestApplyCamera_UserSessionSocketMountsAtSystemPath(t *testing.T) {
	withPipeWire(t, "/run/user/1000/pipewire-0")
	withPipeWireGroup(t, 997, true)

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	applyCamera(spec)

	for _, m := range spec.Mounts {
		if m.Destination == "/run/pipewire/pipewire-0" {
			if m.Source != "/run/user/1000/pipewire-0" {
				t.Errorf("mount source = %q, want the user-session socket", m.Source)
			}
			return
		}
	}
	t.Error("camera entitlement did not mount the user-session PipeWire socket")
}

// Cameras must keep working on a host with no PipeWire — the agent falls back
// to opening the device directly, and so does the container.
func TestApplyCamera_NoPipeWireLeavesSpecAlone(t *testing.T) {
	withPipeWire(t, "")
	withPipeWireGroup(t, 997, true)

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	applyCamera(spec)

	if hasMountDest(spec, "/run/pipewire/pipewire-0") {
		t.Error("mounted a PipeWire socket that does not exist")
	}
	if hasEnv(spec, "PIPEWIRE_RUNTIME_DIR") {
		t.Error("pointed the container at a PipeWire that is not running")
	}
	if hasGID(spec, 997) {
		t.Error("added the pipewire group with no PipeWire on the host")
	}
}

// The GID is image-build allocated, so a host without the group is normal and
// must not stop the mount — a root container still reaches the socket.
func TestApplyCamera_MissingPipeWireGroupStillMounts(t *testing.T) {
	withPipeWire(t, "/run/pipewire/pipewire-0")
	withPipeWireGroup(t, 0, false)

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	applyCamera(spec)

	if !hasMountDest(spec, "/run/pipewire/pipewire-0") {
		t.Error("an unresolvable pipewire group must not skip the socket mount")
	}
	if hasGID(spec, 0) {
		t.Error("added GID 0 when the group lookup failed")
	}
}

func TestApplyAudio_MountsPipeWireSocket(t *testing.T) {
	withPipeWire(t, "/run/pipewire/pipewire-0")
	withPipeWireGroup(t, 997, true)

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	applyAudio(spec)

	if !hasMountDest(spec, "/run/pipewire/pipewire-0") {
		t.Error("audio entitlement did not mount the PipeWire socket")
	}
	if !hasEnv(spec, "PIPEWIRE_RUNTIME_DIR") {
		t.Error("audio entitlement did not set PIPEWIRE_RUNTIME_DIR")
	}
}

// The system-wide daemon does NOT put the two sockets side by side: PipeWire
// listens on /run/pipewire/pipewire-0 while pipewire-pulse listens on
// /run/pulse/native. Deriving one from the other finds nothing and drops audio.
func TestPulseSocketHostPath_SystemLayout(t *testing.T) {
	withHostSockets(t, "/run/pipewire/pipewire-0", "/run/pulse/native")

	if got := pulseSocketHostPath("/run/pipewire/pipewire-0"); got != "/run/pulse/native" {
		t.Errorf("pulseSocketHostPath = %q, want /run/pulse/native", got)
	}
}

// In a user session the two really are siblings.
func TestPulseSocketHostPath_UserSessionLayout(t *testing.T) {
	withHostSockets(t, "/run/user/1000/pipewire-0", "/run/user/1000/pulse/native")

	if got := pulseSocketHostPath("/run/user/1000/pipewire-0"); got != "/run/user/1000/pulse/native" {
		t.Errorf("pulseSocketHostPath = %q, want the sibling socket", got)
	}
}

func TestPulseSocketHostPath_AbsentPulse(t *testing.T) {
	withHostSockets(t, "/run/pipewire/pipewire-0")

	if got := pulseSocketHostPath("/run/pipewire/pipewire-0"); got != "" {
		t.Errorf("pulseSocketHostPath = %q, want empty when pulse is not running", got)
	}
}

func TestApplyAudio_MountsPulseCompatSocket(t *testing.T) {
	withPipeWire(t, "/run/pipewire/pipewire-0")
	withPipeWireGroup(t, 997, true)
	withHostSockets(t, "/run/pipewire/pipewire-0", "/run/pulse/native")

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	applyAudio(spec)

	if !hasMountDest(spec, ctrPulseSocket) {
		t.Error("audio entitlement did not mount the PulseAudio compat socket")
	}
	if !hasEnv(spec, "PULSE_SERVER") {
		t.Error("audio entitlement did not set PULSE_SERVER")
	}
}

// An app declaring audio and camera applies both, and each wants the socket.
func TestMountPipeWireSocket_IsIdempotent(t *testing.T) {
	withPipeWire(t, "/run/pipewire/pipewire-0")
	withPipeWireGroup(t, 997, true)
	withHostSockets(t, "/run/pipewire/pipewire-0", "/run/pulse/native")

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	applyAudio(spec)
	applyCamera(spec)

	mounts := 0
	for _, m := range spec.Mounts {
		if m.Destination == "/run/pipewire/pipewire-0" {
			mounts++
		}
	}
	if mounts != 1 {
		t.Errorf("PipeWire socket mounted %d times, want 1 — runc stacks every one", mounts)
	}

	envs := 0
	for _, e := range spec.Process.Env {
		if strings.HasPrefix(e, "PIPEWIRE_RUNTIME_DIR=") {
			envs++
		}
	}
	if envs != 1 {
		t.Errorf("PIPEWIRE_RUNTIME_DIR set %d times, want 1", envs)
	}
}
