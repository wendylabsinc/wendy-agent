package oci

import "testing"

func TestDedupeDevices(t *testing.T) {
	spec := &Spec{Linux: &Linux{Devices: []LinuxDevice{
		{Path: "/dev/nvidiactl", Major: 195, Minor: 255},
		{Path: "/dev/nvmap", Major: 10, Minor: 55},
		{Path: "/dev/nvidiactl", Major: 195, Minor: 255}, // dup from a second provisioner
		{Path: "/dev/nvgpu/igpu0/ctrl", Major: 234, Minor: 1},
	}}}

	DedupeDevices(spec)

	if got := len(spec.Linux.Devices); got != 3 {
		t.Fatalf("device count = %d, want 3", got)
	}
	// First occurrence is kept and order is otherwise preserved.
	want := []string{"/dev/nvidiactl", "/dev/nvmap", "/dev/nvgpu/igpu0/ctrl"}
	for i, w := range want {
		if spec.Linux.Devices[i].Path != w {
			t.Errorf("device[%d] = %q, want %q", i, spec.Linux.Devices[i].Path, w)
		}
	}
}

func TestDedupeDevices_NoLinuxOrEmpty(t *testing.T) {
	// Must not panic on nil Linux or short slices.
	DedupeDevices(&Spec{})
	DedupeDevices(&Spec{Linux: &Linux{}})
	DedupeDevices(&Spec{Linux: &Linux{Devices: []LinuxDevice{{Path: "/dev/x"}}}})
}
