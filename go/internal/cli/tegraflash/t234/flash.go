//go:build darwin || linux

package t234

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Stage-2 timeouts. The first flashpkg LUN appears once the RCM-booted
// kernel+initrd are up (~30 s); the final one only after the device finishes
// programming its QSPI boot flash in the background.
const (
	flashpkgWait    = 5 * time.Minute
	rootfsWait      = 5 * time.Minute
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
	return s.release(ctx, disk)
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
	if err := s.RunHelper([]string{"--release", "--serial", disk.Serial}, nil); err != nil {
		return fmt.Errorf("releasing %s: %w", disk.DevPath, err)
	}
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
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("%s did not disconnect after release", disk.DevPath)
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
