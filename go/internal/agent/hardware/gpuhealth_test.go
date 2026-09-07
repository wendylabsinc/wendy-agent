package hardware

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A probe on a host with no accelerator must decline to answer rather than
// invent a verdict — the caller reports nothing at all in that case.
func TestProbeGPUDriver_NoAcceleratorDeclines(t *testing.T) {
	if _, err := os.Stat("/dev/kfd"); err == nil {
		t.Skip("host has an AMD compute device")
	}
	if _, err := os.Stat("/etc/nv_tegra_release"); err == nil {
		t.Skip("host is a Jetson")
	}
	if _, err := os.Stat("/dev/nvidiactl"); err == nil {
		t.Skip("host has an NVIDIA driver")
	}

	if h, ok := ProbeGPUDriver(context.Background()); ok {
		t.Errorf("ProbeGPUDriver reported %+v on a host with no accelerator; want no answer", h)
	}
}

// openable is the whole point of the probe: a node that stats fine but cannot be
// opened is exactly the state a presence check cannot see.
func TestOpenable_DistinguishesStatFromOpen(t *testing.T) {
	// A path that exists and opens.
	if err := openable(os.DevNull); err != nil {
		t.Errorf("openable(%s) = %v; want nil", os.DevNull, err)
	}

	// A path that does not exist at all.
	if err := openable(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("openable(absent path) = nil; want an error")
	}

	// A path that stats but refuses to open: a directory opened O_RDONLY is
	// permitted, so use an unreadable file, which stat sees and open rejects.
	unreadable := filepath.Join(t.TempDir(), "unreadable")
	if err := os.WriteFile(unreadable, []byte("x"), 0o000); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	if _, err := os.Stat(unreadable); err != nil {
		t.Fatalf("fixture should stat: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; mode 0000 does not deny open")
	}
	if err := openable(unreadable); err == nil {
		t.Error("openable(unreadable) = nil; want the open to fail where the stat succeeded")
	}
}

func TestGPUDriverDescription_NamesTheVerdict(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{
		{driverStatusResponding, "responding"},
		{driverStatusNotResponding, "NOT responding"},
		{driverStatusAbsent, "absent"},
	} {
		got := gpuDriverDescription(GPUDriverHealth{Vendor: "nvidia", Status: tc.status})
		if !strings.Contains(got, tc.want) {
			t.Errorf("description for %q = %q; want it to contain %q", tc.status, got, tc.want)
		}
		if !strings.Contains(got, "nvidia") {
			t.Errorf("description for %q = %q; want the vendor named", tc.status, got)
		}
	}
}

// A wedged driver can be verbose; the verdict must stay readable.
func TestTruncate_KeepsDetailBounded(t *testing.T) {
	got := truncate(strings.Repeat("x", 500) + "\nsecond line")
	if len(got) > 210 {
		t.Errorf("len = %d; want it bounded", len(got))
	}
	if strings.Contains(got, "\n") {
		t.Error("detail kept a newline; it goes into a single-line property")
	}
}
