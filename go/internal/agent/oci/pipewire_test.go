package oci

import "testing"

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
// /dev/videoN alone gives the container the kernel's single capture slot, which
// is the contention WDY-1994 is about.
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
