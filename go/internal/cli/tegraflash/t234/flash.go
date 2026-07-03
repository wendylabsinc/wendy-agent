//go:build darwin || linux

package t234

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Stage-2 timeouts. The first flashpkg LUN appears once the RCM-booted
// kernel+initrd are up (~30 s); the rootfs LUN only after the device has run
// the earlier command-sequence steps (bootloader → QSPI programming, pre-wipe),
// which can take many minutes on the device before it exports mmcblk0; the
// final status LUN only after all programming completes.
const (
	flashpkgWait    = 5 * time.Minute
	rootfsWait      = 15 * time.Minute
	finalStatusWait = 15 * time.Minute
	disappearWait   = 45 * time.Second
)

// ErrDeviceSideFailed reports that the device exported its status package
// early, i.e. its side of the flash aborted before the rootfs write.
var ErrDeviceSideFailed = errors.New("device-side flash failed early")

// Stage2 drives the mass-storage half of the flash (everything after the RCM
// boot). Callbacks keep the privileged pieces and the UI in the caller.
type Stage2 struct {
	BundleDir string
	Plan      *Plan
	Out       io.Writer    // verbose log
	Detail    func(string) // live one-line progress; may be nil

	// RunHelper runs the hidden root helper `wendy __t234-write` with args,
	// reporting any "PROGRESS <done> <total>" lines it prints.
	RunHelper func(args []string, onProgress func(done, total int64)) error
}

func (s *Stage2) detail(format string, args ...any) {
	if s.Detail != nil {
		s.Detail(fmt.Sprintf(format, args...))
	}
}

// SendFlashPackage waits for the device's "flashpkg" LUN, replaces it with
// the prep-built command package, and releases it so the device starts
// executing the command sequence.
func (s *Stage2) SendFlashPackage(ctx context.Context) error {
	s.detail("waiting for the flash-package disk")
	fmt.Fprintln(s.Out, "Waiting for the device's flashpkg USB disk...")
	disk, err := WaitForUMSDisk(ctx, FlashpkgVendor, flashpkgWait)
	if err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "  flashpkg disk: %s (%d bytes, session %s)\n", disk.DevPath, disk.SizeBytes, disk.Serial)
	if disk.SizeBytes > 0 && disk.SizeBytes < flashpkgSize {
		return fmt.Errorf("flashpkg disk %s is smaller (%d bytes) than the flash package (%d bytes)", disk.DevPath, disk.SizeBytes, flashpkgSize)
	}

	unmountUMSDisk(disk)
	s.detail("sending flash commands + bootloader")
	if err := s.RunHelper([]string{"--device", disk.RawPath, "--blob", FlashpkgPath(s.BundleDir)}, nil); err != nil {
		return fmt.Errorf("writing flash package: %w", err)
	}
	if err := s.verifyFlashPackage(disk); err != nil {
		return err
	}
	return s.release(ctx, disk)
}

// verifyFlashPackage reads the flash package back from the device right after
// writing it and confirms the command sequence parses and matches what we sent.
// We overwrite the whole 128 MiB LUN as a raw ext4 image — macOS can't mount
// ext4 to edit files in place the way the reference Linux host script does — so
// if that image doesn't land as a filesystem the device can parse, the initrd
// reads a broken command mailbox and stalls without exporting anything, which
// is indistinguishable from a device-side hang until we check. Reading it back
// turns that ambiguity into a clear host-side verdict.
func (s *Stage2) verifyFlashPackage(disk UMSDisk) error {
	local, err := os.Open(FlashpkgPath(s.BundleDir))
	if err != nil {
		return err
	}
	defer local.Close()
	want, err := Ext4ReadFile(local, "flashpkg/conf/command_sequence")
	if err != nil {
		return fmt.Errorf("reading local command_sequence: %w", err)
	}

	tmp, err := os.CreateTemp("", "t234-verify-*.ext4")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	s.detail("verifying flash package on device")
	if err := s.RunHelper([]string{"--device", disk.RawPath, "--dump", tmpPath, "--bytes", fmt.Sprint(int64(flashpkgSize))}, nil); err != nil {
		return fmt.Errorf("reading back flash package: %w", err)
	}
	img, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer img.Close()
	got, err := Ext4ReadFile(img, "flashpkg/conf/command_sequence")
	if err != nil {
		return fmt.Errorf("the flash package did not land on the device as a valid filesystem "+
			"(can't read command_sequence back: %w) — the raw USB write is corrupt", err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		return fmt.Errorf("flash package verification failed: device command_sequence is %q, expected %q",
			strings.TrimSpace(string(got)), strings.TrimSpace(string(want)))
	}
	fmt.Fprintln(s.Out, "  verified flash package on device (command sequence intact)")
	return nil
}

// WriteRootfsDevice waits for the exported eMMC LUN and writes the GPT and
// every partition image, then releases it. If the device exports its status
// package instead, it returns ErrDeviceSideFailed (collect logs via
// AwaitFinalStatus).
func (s *Stage2) WriteRootfsDevice(ctx context.Context) error {
	s.detail("waiting for the %s disk", s.Plan.RootfsDevice)
	fmt.Fprintf(s.Out, "Waiting for the device to export %s over USB...\n", s.Plan.RootfsDevice)
	disk, err := waitForUMSDiskConfirmed(ctx, s.Plan.RootfsDevice, rootfsWait)
	if err != nil {
		if errors.Is(err, errGotFlashpkg) {
			return ErrDeviceSideFailed
		}
		return err
	}
	fmt.Fprintf(s.Out, "  %s: %s (%d bytes)\n", s.Plan.RootfsDevice, disk.DevPath, disk.SizeBytes)
	if min := s.Plan.MinDeviceSectors() * sectorSize; disk.SizeBytes > 0 && disk.SizeBytes < min {
		return fmt.Errorf("exported %s (%d bytes) is smaller than the flash layout (%d bytes)", s.Plan.RootfsDevice, disk.SizeBytes, min)
	}

	unmountUMSDisk(disk)
	fmt.Fprintf(s.Out, "Writing GPT + %d partitions...\n", len(s.Plan.Partitions))
	start := time.Now()
	err = s.RunHelper([]string{"--device", disk.RawPath, "--write-plan", "--bundle", s.BundleDir},
		func(done, total int64) {
			if total > 0 {
				s.detail("%.1f/%.1f GiB", float64(done)/(1<<30), float64(total)/(1<<30))
			}
		})
	if err != nil {
		return fmt.Errorf("writing %s: %w", s.Plan.RootfsDevice, err)
	}
	fmt.Fprintf(s.Out, "  partitions written in %v\n", time.Since(start).Round(time.Second))
	// macOS re-probes the disk when the writer closes it and may auto-mount
	// the freshly written FAT config partition; unmount before releasing.
	unmountUMSDisk(disk)
	return s.release(ctx, disk)
}

// FinalStatus is the device-side outcome of the flash.
type FinalStatus struct {
	Success bool
	Status  string            // raw contents of flashpkg/status
	Logs    map[string][]byte // device-side logs, by filename
}

// AwaitFinalStatus waits for the device to re-export the flash package,
// reads the status and device-side logs out of it, and releases the disk
// (which lets the device reboot into the freshly flashed OS).
func (s *Stage2) AwaitFinalStatus(ctx context.Context) (*FinalStatus, error) {
	s.detail("waiting for the device's final status")
	fmt.Fprintln(s.Out, "Waiting for the device to report its final status (QSPI programming can take several minutes)...")
	disk, err := WaitForUMSDisk(ctx, FlashpkgVendor, finalStatusWait)
	if err != nil {
		return nil, err
	}
	unmountUMSDisk(disk)

	tmp, err := os.CreateTemp("", "t234-flashpkg-*.ext4")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	s.detail("reading device status + logs")
	if err := s.RunHelper([]string{"--device", disk.RawPath, "--dump", tmpPath, "--bytes", fmt.Sprint(int64(flashpkgSize))}, nil); err != nil {
		return nil, fmt.Errorf("reading back flash package: %w", err)
	}
	if err := s.release(ctx, disk); err != nil {
		// The flash outcome is already on disk; a failed release only delays
		// the device's automatic reboot. Report it but don't fail the flash.
		fmt.Fprintf(s.Out, "  note: releasing the status disk failed (%v); power-cycle the device manually\n", err)
	}

	img, err := os.Open(tmpPath)
	if err != nil {
		return nil, err
	}
	defer img.Close()

	res := &FinalStatus{Logs: map[string][]byte{}}
	statusBytes, err := Ext4ReadFile(img, "flashpkg/status")
	if err != nil {
		return nil, fmt.Errorf("reading device status from flash package: %w", err)
	}
	res.Status = strings.TrimSpace(string(statusBytes))
	res.Success = res.Status == "SUCCESS"
	if names, err := Ext4ListDir(img, "flashpkg/logs"); err == nil {
		for _, name := range names {
			if data, err := Ext4ReadFile(img, "flashpkg/logs/"+name); err == nil {
				res.Logs[name] = data
			}
		}
	}
	return res, nil
}

// release forces the USB-level disconnect the device's initrd waits for,
// then waits for the disk node to actually go away so the next wait can't
// match a stale LUN.
func (s *Stage2) release(ctx context.Context, disk UMSDisk) error {
	fmt.Fprintf(s.Out, "  releasing %s\n", disk.DevPath)
	// Primary: a SCSI eject (START STOP UNIT / power-off), matching the vendor
	// initrd-flash host script. This is the clean per-LUN "host is done" the
	// device's initrd waits for before finalizing the LUN and moving to its
	// next command — e.g. exporting the rootfs device. A USB-level disconnect
	// alone makes the device leave the flashpkg but can be too blunt for it to
	// then bring up the next LUN.
	ejectUMSDisk(disk)
	gone, err := s.waitForDiskGone(ctx, disk)
	if err != nil {
		return err
	}
	if gone {
		return nil
	}

	// Fallback: force a USB-level disconnect (the initrd also proceeds when the
	// UDC leaves "configured"). Needed when the eject didn't take — e.g. no
	// udisks/diskutil or a driver holding the node.
	fmt.Fprintf(s.Out, "  eject didn't release %s; forcing a USB disconnect\n", disk.DevPath)
	if err := s.RunHelper([]string{"--release", "--serial", disk.Serial}, nil); err != nil {
		return fmt.Errorf("releasing %s: %w", disk.DevPath, err)
	}
	gone, err = s.waitForDiskGone(ctx, disk)
	if err != nil {
		return err
	}
	if gone {
		return nil
	}
	return fmt.Errorf("%s did not disconnect after release", disk.DevPath)
}

// waitForDiskGone polls until the LUN's device node disappears (up to
// disappearWait) so the next wait can't match a stale node. Returns (false,
// nil) on timeout and (false, ctx.Err()) if the context is cancelled.
func (s *Stage2) waitForDiskGone(ctx context.Context, disk UMSDisk) (bool, error) {
	deadline := time.Now().Add(disappearWait)
	for time.Now().Before(deadline) {
		disks, err := listUMSDisks()
		gone := err == nil
		for _, d := range disks {
			if d.DevPath == disk.DevPath && d.Vendor == disk.Vendor {
				gone = false
			}
		}
		if gone {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return false, nil
}

// errGotFlashpkg distinguishes "the device exported its status package
// instead of the requested LUN" inside waitForUMSDiskConfirmed.
var errGotFlashpkg = errors.New("got flashpkg instead of the requested LUN")

// waitForUMSDiskConfirmed is WaitForUMSDisk, except a flashpkg sighting is
// only treated as a device-side failure when it persists across scans (a
// just-released flashpkg node can linger for a moment on the host).
func waitForUMSDiskConfirmed(ctx context.Context, vendor string, timeout time.Duration) (UMSDisk, error) {
	deadline := time.Now().Add(timeout)
	flashpkgStreak := 0
	for {
		disks, err := listUMSDisks()
		if err == nil {
			var match *UMSDisk
			sawFlashpkg := false
			for i, d := range disks {
				if d.Vendor == vendor {
					match = &disks[i]
				}
				if d.Vendor == FlashpkgVendor {
					sawFlashpkg = true
				}
			}
			if match != nil {
				return *match, nil
			}
			if sawFlashpkg {
				flashpkgStreak++
				if flashpkgStreak >= 5 {
					return UMSDisk{}, errGotFlashpkg
				}
			} else {
				flashpkgStreak = 0
			}
		}
		if time.Now().After(deadline) {
			return UMSDisk{}, fmt.Errorf("timed out waiting for USB storage %q from the device\n%s", vendor, observedUMSHint())
		}
		select {
		case <-ctx.Done():
			return UMSDisk{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
