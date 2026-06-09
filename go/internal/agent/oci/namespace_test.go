package oci

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// requireLinux skips the test on non-Linux platforms where /proc is unavailable.
func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("procfs namespace paths are only available on Linux (running %s)", runtime.GOOS)
	}
}

func TestJoinGroupNamespaces_SharedIPC(t *testing.T) {
	requireLinux(t)
	// Use the current process PID — guaranteed to have valid namespace paths.
	pid := uint32(os.Getpid())
	spec := DefaultSpec("rootfs", []string{"/bin/sh"})
	anchors, err := JoinGroupNamespaces(spec, pid, "shared-ipc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		for _, f := range anchors {
			f.Close()
		}
	}()
	// shared-ipc joins ipc, network, uts — 3 anchors.
	if len(anchors) != 3 {
		t.Errorf("shared-ipc: got %d fd anchors, want 3", len(anchors))
	}

	nsMap := make(map[string]string)
	for _, ns := range spec.Linux.Namespaces {
		nsMap[ns.Type] = ns.Path
	}
	// Paths must be fd-anchored (/proc/self/fd/{n}) to prevent TOCTOU PID reuse.
	for _, nsType := range []string{"ipc", "network", "uts"} {
		if !strings.HasPrefix(nsMap[nsType], "/proc/self/fd/") {
			t.Errorf("%s namespace path = %q, want /proc/self/fd/<n>", nsType, nsMap[nsType])
		}
	}
	if nsMap["pid"] != "" {
		t.Errorf("pid namespace should not be joined, got %q", nsMap["pid"])
	}
}

func TestJoinGroupNamespaces_SharedNetwork(t *testing.T) {
	requireLinux(t)
	pid := uint32(os.Getpid())
	spec := DefaultSpec("rootfs", []string{"/bin/sh"})
	anchors, err := JoinGroupNamespaces(spec, pid, "shared-network")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		for _, f := range anchors {
			f.Close()
		}
	}()
	// shared-network joins network and uts — 2 anchors.
	if len(anchors) != 2 {
		t.Errorf("shared-network: got %d fd anchors, want 2", len(anchors))
	}

	nsMap := make(map[string]string)
	for _, ns := range spec.Linux.Namespaces {
		nsMap[ns.Type] = ns.Path
	}
	// Paths must be fd-anchored (/proc/self/fd/{n}) to prevent TOCTOU PID reuse.
	for _, nsType := range []string{"network", "uts"} {
		if !strings.HasPrefix(nsMap[nsType], "/proc/self/fd/") {
			t.Errorf("%s namespace path = %q, want /proc/self/fd/<n>", nsType, nsMap[nsType])
		}
	}
	// ipc should remain isolated in shared-network mode
	if nsMap["ipc"] != "" {
		t.Errorf("ipc namespace should not be joined in shared-network mode, got %q", nsMap["ipc"])
	}
}

func TestJoinGroupNamespaces_Isolated(t *testing.T) {
	// "isolated" mode is a no-op — no namespace paths are set regardless of
	// platform, so no requireLinux needed.
	spec := DefaultSpec("rootfs", []string{"/bin/sh"})
	anchors, err := JoinGroupNamespaces(spec, 9999, "isolated")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(anchors) != 0 {
		t.Errorf("isolated mode should return no fd anchors, got %d", len(anchors))
	}
	for _, ns := range spec.Linux.Namespaces {
		if ns.Path != "" {
			t.Errorf("isolated mode should not join any namespaces, but %q has path %q", ns.Type, ns.Path)
		}
	}
}

func TestJoinGroupNamespaces_ZeroPID(t *testing.T) {
	spec := DefaultSpec("rootfs", []string{"/bin/sh"})
	if _, err := JoinGroupNamespaces(spec, 0, "shared-ipc"); err == nil {
		t.Fatal("expected error for zero PID, got nil")
	}
}

func TestJoinGroupNamespaces_NilLinux(t *testing.T) {
	spec := &Spec{}
	if _, err := JoinGroupNamespaces(spec, 1234, "shared-ipc"); err == nil {
		t.Fatal("expected error for nil Linux, got nil")
	}
}

func TestJoinGroupNamespaces_StaleProcess(t *testing.T) {
	requireLinux(t)
	// PID 2^22 is beyond the Linux default PID limit (4M) and will not exist.
	spec := DefaultSpec("rootfs", []string{"/bin/sh"})
	if _, err := JoinGroupNamespaces(spec, 1<<22, "shared-ipc"); err == nil {
		t.Fatal("expected error for non-existent PID, got nil")
	}
}

func TestSharedSHMMount(t *testing.T) {
	m := SharedSHMMount("/run/wendy/shm/com.example.app")
	if m.Destination != "/dev/shm" {
		t.Errorf("Destination = %q, want /dev/shm", m.Destination)
	}
	if m.Type != "bind" {
		t.Errorf("Type = %q, want bind", m.Type)
	}
	if m.Source != "/run/wendy/shm/com.example.app" {
		t.Errorf("Source = %q, want /run/wendy/shm/com.example.app", m.Source)
	}
	found := false
	for _, opt := range m.Options {
		if opt == "rbind" {
			found = true
		}
	}
	if !found {
		t.Errorf("Options %v should contain 'rbind'", m.Options)
	}
}

func TestRemoveDefaultSHM(t *testing.T) {
	spec := DefaultSpec("rootfs", []string{"/bin/sh"})
	// Verify /dev/shm exists before removal.
	hasSHM := false
	for _, m := range spec.Mounts {
		if m.Destination == "/dev/shm" {
			hasSHM = true
		}
	}
	if !hasSHM {
		t.Skip("DefaultSpec does not include /dev/shm mount; skipping")
	}
	RemoveDefaultSHM(spec)
	for _, m := range spec.Mounts {
		if m.Destination == "/dev/shm" {
			t.Error("default /dev/shm should be removed after RemoveDefaultSHM")
		}
	}
}
