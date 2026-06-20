package oci

import (
	"fmt"
	"os"
	"runtime"
	"testing"
)

// requireLinux skips the test on non-Linux platforms where /proc is unavailable.
func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("procfs namespace paths are only available on Linux (running %s)", runtime.GOOS)
	}
}

// TestJoinGroupNamespaces_AddsMissingNamespaceEntry guards against a join
// silently no-op'ing when the base spec lacks the namespace entry. The function
// must add a joining entry (Path set) for every namespace the isolation mode
// requires, not only patch entries that happen to already be present.
func TestJoinGroupNamespaces_AddsMissingNamespaceEntry(t *testing.T) {
	requireLinux(t)
	pid := uint32(os.Getpid())
	spec := DefaultSpec("rootfs", []string{"/bin/sh"})

	// Simulate a base spec that has no network namespace entry.
	var filtered []LinuxNamespace
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type != "network" {
			filtered = append(filtered, ns)
		}
	}
	spec.Linux.Namespaces = filtered

	anchors, err := JoinGroupNamespaces(spec, pid, "shared-network")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		for _, f := range anchors {
			f.Close()
		}
	}()

	var netPath string
	found := false
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == "network" {
			netPath, found = ns.Path, true
		}
	}
	if !found {
		t.Fatal("network namespace join was silently skipped: no network entry added to spec")
	}
	want := fmt.Sprintf("/proc/%d/ns/net", pid)
	if netPath != want {
		t.Errorf("network namespace path = %q, want %q", netPath, want)
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
	// Paths must be raw procfs namespace paths: the spec is consumed by runc
	// in a different process, where an agent-local /proc/self/fd/<n> path can
	// never resolve. If the primary exits, runc fails with ENOENT (fail-safe).
	wantNS := map[string]string{"ipc": "ipc", "network": "net", "uts": "uts"}
	for nsType, kernel := range wantNS {
		want := fmt.Sprintf("/proc/%d/ns/%s", pid, kernel)
		if nsMap[nsType] != want {
			t.Errorf("%s namespace path = %q, want %q", nsType, nsMap[nsType], want)
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
	// Paths must be raw procfs namespace paths resolvable from runc's process.
	wantNS := map[string]string{"network": "net", "uts": "uts"}
	for nsType, kernel := range wantNS {
		want := fmt.Sprintf("/proc/%d/ns/%s", pid, kernel)
		if nsMap[nsType] != want {
			t.Errorf("%s namespace path = %q, want %q", nsType, nsMap[nsType], want)
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
