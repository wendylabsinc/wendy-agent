//go:build darwin || linux || windows

package t234

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func withUMSScan(t *testing.T, scan func() ([]UMSDisk, error)) {
	t.Helper()
	previous := scanUMSDisks
	scanUMSDisks = scan
	t.Cleanup(func() { scanUMSDisks = previous })
}

func withFastUMSPoll(t *testing.T) {
	t.Helper()
	previous := umsPollInterval
	umsPollInterval = time.Millisecond
	t.Cleanup(func() { umsPollInterval = previous })
}

func TestWaitForUMSDiskAtCorrelatesPortAndSession(t *testing.T) {
	withUMSScan(t, func() ([]UMSDisk, error) {
		return []UMSDisk{
			{DevPath: "/dev/wrong", Vendor: "nvme0n1", PortPath: "1-2", Serial: "aaaaaaaa"},
			{DevPath: "/dev/right", Vendor: "nvme0n1", PortPath: "1-3", Serial: "bbbbbbbb"},
		}, nil
	})
	disk, err := WaitForUMSDiskAt(context.Background(), LUNSelector{Vendor: "nvme0n1", PortPath: "1-3", Session: "BBBBBBBB"}, time.Second)
	if err != nil || disk.DevPath != "/dev/right" {
		t.Fatalf("correlated disk = %+v, err=%v", disk, err)
	}
}

func TestWaitForUMSDiskAtRejectsMissingTopology(t *testing.T) {
	withUMSScan(t, func() ([]UMSDisk, error) {
		return []UMSDisk{{DevPath: "/dev/sdz", Vendor: FlashpkgVendor, Serial: "12345678"}}, nil
	})
	_, err := WaitForUMSDiskAt(context.Background(), LUNSelector{Vendor: FlashpkgVendor, PortPath: "1-3"}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "physical USB port could not be determined") {
		t.Fatalf("missing-topology error = %v", err)
	}
}

func TestWaitForUMSDiskAtRejectsAmbiguity(t *testing.T) {
	withUMSScan(t, func() ([]UMSDisk, error) {
		return []UMSDisk{
			{DevPath: "/dev/a", Vendor: FlashpkgVendor, PortPath: "1-3", Serial: "12345678"},
			{DevPath: "/dev/b", Vendor: FlashpkgVendor, PortPath: "1-3", Serial: "12345678"},
		}, nil
	})
	_, err := WaitForUMSDiskAt(context.Background(), LUNSelector{Vendor: FlashpkgVendor, PortPath: "1-3", Session: "12345678"}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguity error = %v", err)
	}
}

func TestValidateDeviceIdentity(t *testing.T) {
	want := IdentityExpectation{ModuleID: "3701", ModuleSKU: "0005", CarrierID: "3737", CarrierSKU: "0000"}
	got := DeviceIdentity{Protocol: DeviceIdentityProtocol, SessionID: "ABCDEF12", ModuleID: "3701", ModuleSKU: "0005", CarrierID: "3737", CarrierSKU: "0000"}
	if err := validateDeviceIdentity(got, "abcdef12", want); err != nil {
		t.Fatal(err)
	}
	got.ModuleSKU = "0004"
	if err := validateDeviceIdentity(got, "abcdef12", want); err == nil || !strings.Contains(err.Error(), "wrong Jetson hardware") {
		t.Fatalf("wrong-SKU error = %v", err)
	}
	got.ModuleSKU = "0005"
	got.SessionID = "00000000"
	if err := validateDeviceIdentity(got, "abcdef12", want); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong-session error = %v", err)
	}
	got.SessionID = "abcdef12"
	got.Protocol = "legacy"
	if err := validateDeviceIdentity(got, "abcdef12", want); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("wrong-protocol error = %v", err)
	}
	// An initrd that failed to parse its device tree reports UNKNOWN (seen live
	// on an Orin Nano whose DTB compatible carries a "-super" suffix). That is
	// "identity unreadable", not "wrong hardware" — the error must say the
	// flashpack's initrd is at fault, and still refuse the flash.
	got.Protocol = DeviceIdentityProtocol
	got.ModuleID, got.ModuleSKU, got.CarrierID, got.CarrierSKU = "UNKNOWN", "", "UNKNOWN", ""
	err := validateDeviceIdentity(got, "abcdef12", want)
	if err == nil || !strings.Contains(err.Error(), "could not identify") || strings.Contains(err.Error(), "wrong Jetson hardware") {
		t.Fatalf("unknown-identity error = %v", err)
	}
}

func TestRootfsWaitToleratesStaleFlashpkgTransition(t *testing.T) {
	withFastUMSPoll(t)
	calls := 0
	withUMSScan(t, func() ([]UMSDisk, error) {
		calls++
		if calls <= 2 {
			return []UMSDisk{{DevPath: "/dev/flashpkg", Vendor: FlashpkgVendor, PortPath: "1-3", Serial: "12345678"}}, nil
		}
		return []UMSDisk{{DevPath: "/dev/rootfs", Vendor: "nvme0n1", PortPath: "1-3", Serial: "12345678"}}, nil
	})
	disk, err := waitForUMSDiskConfirmed(context.Background(), LUNSelector{Vendor: "nvme0n1", PortPath: "1-3", Session: "12345678"}, time.Second)
	if err != nil || disk.DevPath != "/dev/rootfs" {
		t.Fatalf("transition result = %+v, %v", disk, err)
	}
}

func TestRootfsWaitDetectsPersistentEarlyFinalStatus(t *testing.T) {
	withFastUMSPoll(t)
	withUMSScan(t, func() ([]UMSDisk, error) {
		return []UMSDisk{{DevPath: "/dev/flashpkg", Vendor: FlashpkgVendor, PortPath: "1-3", Serial: "12345678"}}, nil
	})
	_, err := waitForUMSDiskConfirmed(context.Background(), LUNSelector{Vendor: "nvme0n1", PortPath: "1-3", Session: "12345678"}, time.Second)
	if err != errGotFlashpkg {
		t.Fatalf("early-status error = %v", err)
	}
}

func TestUMSWaitCancellationAndTimeout(t *testing.T) {
	withFastUMSPoll(t)
	withUMSScan(t, func() ([]UMSDisk, error) { return nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WaitForUMSDiskAt(ctx, LUNSelector{Vendor: FlashpkgVendor, PortPath: "1-3"}, time.Second); err != context.Canceled {
		t.Fatalf("cancel error = %v", err)
	}
	if _, err := WaitForUMSDiskAt(context.Background(), LUNSelector{Vendor: FlashpkgVendor, PortPath: "1-3"}, 3*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestWaitForDiskGoneTracksExactLUN(t *testing.T) {
	withFastUMSPoll(t)
	calls := 0
	disk := UMSDisk{DevPath: "/dev/rootfs", Vendor: "nvme0n1", PortPath: "1-3", Serial: "12345678"}
	withUMSScan(t, func() ([]UMSDisk, error) {
		calls++
		if calls < 3 {
			return []UMSDisk{disk}, nil
		}
		return nil, nil
	})
	gone, err := (&Stage2{}).waitForDiskGone(context.Background(), disk)
	if err != nil || !gone {
		t.Fatalf("waitForDiskGone = %v, %v", gone, err)
	}
}

func TestAwaitFinalStatusCollectsLogsAndRequiresSuccess(t *testing.T) {
	withFastUMSPoll(t)
	disk := UMSDisk{DevPath: "/dev/fake", RawPath: "/dev/fake", Vendor: FlashpkgVendor, PortPath: "1-3", Serial: "12345678"}
	calls := 0
	withUMSScan(t, func() ([]UMSDisk, error) {
		calls++
		if calls == 1 {
			return []UMSDisk{disk}, nil
		}
		return nil, nil
	})
	fixture, err := io.ReadAll(openFixture(t, "flashpkg-4k.ext4.gz"))
	if err != nil {
		t.Fatal(err)
	}
	stage := &Stage2{
		PortPath: "1-3", Session: "12345678", StatusPath: "flashpkg/status", LogsPath: "flashpkg/logs", Out: io.Discard,
		RunHelper: func(_ context.Context, req HelperRequest, _ func(int64, int64)) error {
			if req.Writer.DumpTo != "" {
				return os.WriteFile(req.Writer.DumpTo, fixture, 0o644)
			}
			return nil
		},
	}
	status, err := stage.AwaitFinalStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Success || status.Status != "SUCCESS" || len(status.Logs["big.log"]) != 300000 {
		t.Fatalf("final status = %+v", status)
	}
}

// After RCM boot the gadget can train at a different USB speed than the
// bootROM's recovery device, and USB2/USB3 phys of one physical connector are
// distinct root-hub ports (seen live on macOS: recovery at usb 1-1, SuperSpeed
// gadget at usb 1-2). The first-LUN wait must treat the recovery port as a
// hint: exact match preferred, a unique off-port candidate accepted, several
// candidates fail closed.
func TestFirstLUNWaitAcceptsUniqueOffPortGadget(t *testing.T) {
	withUMSScan(t, func() ([]UMSDisk, error) {
		return []UMSDisk{{DevPath: "/dev/disk6", Vendor: FlashpkgVendor, PortPath: "1-2", Serial: "f3885343"}}, nil
	})
	disk, err := WaitForUMSDiskAt(context.Background(), LUNSelector{Vendor: FlashpkgVendor, PortPath: "1-1", PortHint: true}, time.Second)
	if err != nil || disk.DevPath != "/dev/disk6" {
		t.Fatalf("off-port gadget = %+v, err=%v", disk, err)
	}
}

func TestFirstLUNWaitPrefersExactPortMatch(t *testing.T) {
	withUMSScan(t, func() ([]UMSDisk, error) {
		return []UMSDisk{
			{DevPath: "/dev/other", Vendor: FlashpkgVendor, PortPath: "3-2", Serial: "aaaaaaaa"},
			{DevPath: "/dev/mine", Vendor: FlashpkgVendor, PortPath: "1-1", Serial: "bbbbbbbb"},
		}, nil
	})
	disk, err := WaitForUMSDiskAt(context.Background(), LUNSelector{Vendor: FlashpkgVendor, PortPath: "1-1", PortHint: true}, time.Second)
	if err != nil || disk.DevPath != "/dev/mine" {
		t.Fatalf("exact-port disk = %+v, err=%v", disk, err)
	}
}

func TestFirstLUNWaitRejectsMultipleOffPortCandidates(t *testing.T) {
	withUMSScan(t, func() ([]UMSDisk, error) {
		return []UMSDisk{
			{DevPath: "/dev/a", Vendor: FlashpkgVendor, PortPath: "1-2", Serial: "aaaaaaaa"},
			{DevPath: "/dev/b", Vendor: FlashpkgVendor, PortPath: "3-2", Serial: "bbbbbbbb"},
		}, nil
	})
	_, err := WaitForUMSDiskAt(context.Background(), LUNSelector{Vendor: FlashpkgVendor, PortPath: "1-1", PortHint: true}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "none is at the recovery port") {
		t.Fatalf("multi-candidate error = %v", err)
	}
}

// Waits after the first LUN pin the gadget's own port + session; an off-port
// LUN must never satisfy them, hint or not.
func TestSubsequentLUNWaitRequiresExactPort(t *testing.T) {
	withFastUMSPoll(t)
	withUMSScan(t, func() ([]UMSDisk, error) {
		return []UMSDisk{{DevPath: "/dev/other", Vendor: "nvme0n1", PortPath: "1-2", Serial: "f3885343"}}, nil
	})
	_, err := WaitForUMSDiskAt(context.Background(), LUNSelector{Vendor: "nvme0n1", PortPath: "1-1", Session: "f3885343"}, 3*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("off-port subsequent wait error = %v", err)
	}
}

func TestAdoptGadgetRepinsPortAndSession(t *testing.T) {
	s := &Stage2{PortPath: "1-1", Out: io.Discard}
	s.adoptGadget(UMSDisk{DevPath: "/dev/disk6", Vendor: FlashpkgVendor, PortPath: "1-2", Serial: "f3885343"})
	if s.PortPath != "1-2" || s.Session != "f3885343" {
		t.Fatalf("adopted state: port=%q session=%q", s.PortPath, s.Session)
	}
}
