package cdi

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/oci"
)

// hostDeviceNumbers returns the major/minor the production path derives for
// path, so the tests assert the refresh mechanics (mismatch detection, cgroup
// re-alignment, Missing classification) without hardcoding numbers that differ
// between platforms and kernels.
func hostDeviceNumbers(t *testing.T, path string) (int, int) {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Skipf("cannot stat %s: %v", path, err)
	}
	return deviceNumbersFromRdev(uint64(st.Rdev))
}

func specWithDevice(path string, major, minor int64) *oci.Spec {
	maj, min := major, minor
	return &oci.Spec{
		Linux: &oci.Linux{
			Devices: []oci.LinuxDevice{{Path: path, Type: "c", Major: major, Minor: minor}},
			Resources: &oci.LinuxResources{
				Devices: []oci.LinuxDeviceCgroup{{
					Allow: true, Type: "c", Major: &maj, Minor: &min, Access: "rwm",
				}},
			},
		},
	}
}

// The case this exists for: a container spec pinned numbers that the host has
// since changed. Both the device entry and its cgroup rule must be rewritten —
// a device the cgroup does not allow is as useless as one that is absent.
func TestRefreshDeviceNumbers_RewritesStalePairAndCgroupRule(t *testing.T) {
	wantMajor, wantMinor := hostDeviceNumbers(t, os.DevNull)
	spec := specWithDevice(os.DevNull, int64(wantMajor)+7, int64(wantMinor)+3)

	got, err := RefreshDeviceNumbers(spec)
	if err != nil {
		t.Fatalf("RefreshDeviceNumbers: %v", err)
	}
	if !got.Changed() {
		t.Fatalf("Changed() = false for a spec with stale numbers; want true (%+v)", got)
	}

	dev := spec.Linux.Devices[0]
	if dev.Major != int64(wantMajor) || dev.Minor != int64(wantMinor) {
		t.Errorf("device = %d:%d; want %d:%d", dev.Major, dev.Minor, wantMajor, wantMinor)
	}
	rule := spec.Linux.Resources.Devices[0]
	if *rule.Major != int64(wantMajor) || *rule.Minor != int64(wantMinor) {
		t.Errorf("cgroup rule = %d:%d; want %d:%d — a device the cgroup blocks is still unusable",
			*rule.Major, *rule.Minor, wantMajor, wantMinor)
	}
}

// A spec that already matches must be left completely alone: the caller
// persists the spec only when something changed, and a spurious rewrite would
// mean a containerd write on every start.
func TestRefreshDeviceNumbers_NoopWhenCurrent(t *testing.T) {
	major, minor := hostDeviceNumbers(t, os.DevNull)
	spec := specWithDevice(os.DevNull, int64(major), int64(minor))

	got, err := RefreshDeviceNumbers(spec)
	if err != nil {
		t.Fatalf("RefreshDeviceNumbers: %v", err)
	}
	if got.Changed() {
		t.Errorf("Changed() = true for an up-to-date spec; want false (%+v)", got)
	}
	if len(got.Missing) != 0 {
		t.Errorf("Missing = %v for an existing device; want none", got.Missing)
	}
}

// A device that is genuinely gone is reported, not rewritten. There is nothing
// to re-point it at, and silently zeroing it is the failure mode this package
// was changed to stop producing.
func TestRefreshDeviceNumbers_ReportsMissingWithoutRewriting(t *testing.T) {
	spec := specWithDevice(filepath.Join(t.TempDir(), "nvgpu-does-not-exist"), 497, 16)

	got, err := RefreshDeviceNumbers(spec)
	if err != nil {
		t.Fatalf("RefreshDeviceNumbers: %v", err)
	}
	if got.Changed() {
		t.Errorf("Changed() = true for an absent device; want false")
	}
	if len(got.Missing) != 1 {
		t.Fatalf("Missing = %v; want the one absent path", got.Missing)
	}
	if dev := spec.Linux.Devices[0]; dev.Major != 497 || dev.Minor != 16 {
		t.Errorf("device = %d:%d; want the original 497:16 left untouched", dev.Major, dev.Minor)
	}
}

// A regular file where a device node used to be must count as missing. Its
// rdev is 0, so deriving numbers from it would reintroduce the 0:0 placeholder
// through the back door.
func TestRefreshDeviceNumbers_RegularFileIsMissingNotZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-device")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	spec := specWithDevice(path, 497, 16)

	got, err := RefreshDeviceNumbers(spec)
	if err != nil {
		t.Fatalf("RefreshDeviceNumbers: %v", err)
	}
	if got.Changed() {
		t.Errorf("Changed() = true for a regular file; want false")
	}
	if len(got.Missing) != 1 {
		t.Errorf("Missing = %v; want the regular-file path reported as missing", got.Missing)
	}
	if dev := spec.Linux.Devices[0]; dev.Major == 0 && dev.Minor == 0 {
		t.Error("device rewritten to 0:0 from a regular file's rdev; that is the placeholder this must never produce")
	}
}

// A wildcard cgroup rule (no major/minor) must survive untouched.
func TestRefreshDeviceNumbers_LeavesWildcardCgroupRules(t *testing.T) {
	major, minor := hostDeviceNumbers(t, os.DevNull)
	spec := specWithDevice(os.DevNull, int64(major)+7, int64(minor))
	spec.Linux.Resources.Devices = append(spec.Linux.Resources.Devices,
		oci.LinuxDeviceCgroup{Allow: false, Type: "c", Access: "rwm"})

	if _, err := RefreshDeviceNumbers(spec); err != nil {
		t.Fatalf("RefreshDeviceNumbers: %v", err)
	}
	wildcard := spec.Linux.Resources.Devices[1]
	if wildcard.Major != nil || wildcard.Minor != nil {
		t.Errorf("wildcard rule gained numbers %v:%v; want it left alone", wildcard.Major, wildcard.Minor)
	}
}

func TestRefreshDeviceNumbers_ToleratesEmptySpec(t *testing.T) {
	if _, err := RefreshDeviceNumbers(nil); err != nil {
		t.Errorf("RefreshDeviceNumbers(nil) = %v; want no error", err)
	}
	if _, err := RefreshDeviceNumbers(&oci.Spec{}); err != nil {
		t.Errorf("RefreshDeviceNumbers(spec without Linux) = %v; want no error", err)
	}
}
