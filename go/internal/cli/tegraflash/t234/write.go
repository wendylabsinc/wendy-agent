//go:build darwin || linux

package t234

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// This file is the body of the hidden `wendy __t234-write` helper: it runs as
// root (re-exec'd via sudo, like `__bmap-write`) and performs the raw block
// I/O of the flash — writing the flashpkg image, writing the GPT + partition
// images to the exported eMMC, and dumping the flashpkg back for the status
// readback. Progress goes to stdout as "PROGRESS <bytes> <total>" lines the
// parent parses.

// WriterOptions selects what the helper does. Exactly one of Blob,
// WritePlan, or DumpTo is set.
type WriterOptions struct {
	Device    string // raw block device, e.g. /dev/rdisk4
	Blob      string // write this file at offset 0
	WritePlan bool   // write the bundle's GPT + partition images
	BundleDir string // bundle root (holds wendy-prep/plan.json + images)
	DumpTo    string // read DumpBytes from the device into this file
	DumpBytes int64
	Progress  io.Writer // "PROGRESS <bytes> <total>" lines; may be nil
}

const writeChunk = 4 << 20 // 4 MiB write buffer (padded to full sectors)

// rawSyncError classifies the error from flushing a raw block device. fsync
// succeeds on Linux block devices, but macOS raw character devices
// (/dev/rdiskN) reject it with ENOTTY — those writes are unbuffered and have
// already reached the device, so a missing fsync is harmless. ENOTTY (however
// os.File.Sync wraps it) is treated as success; any other error is real.
func rawSyncError(devPath string, err error) error {
	if err == nil || errors.Is(err, syscall.ENOTTY) {
		return nil
	}
	return fmt.Errorf("syncing %s: %w", devPath, err)
}

// RunWriter executes the selected operation. It requires root for raw block
// device access on both macOS and Linux.
func RunWriter(opts WriterOptions) error {
	switch {
	case opts.Blob != "":
		return writeBlob(opts)
	case opts.WritePlan:
		return writePlan(opts)
	case opts.DumpTo != "":
		return dumpDevice(opts)
	default:
		return fmt.Errorf("nothing to do: need --blob, --write-plan, or --dump")
	}
}

// writeBlob writes a single file verbatim at offset 0 (the flashpkg LUN).
func writeBlob(opts WriterOptions) error {
	dev, err := os.OpenFile(opts.Device, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("opening %s (requires root): %w", opts.Device, err)
	}
	defer dev.Close()

	size, err := blockDeviceSize(dev)
	if err != nil {
		return fmt.Errorf("sizing %s: %w", opts.Device, err)
	}
	st, err := os.Stat(opts.Blob)
	if err != nil {
		return err
	}
	if st.Size() > size {
		return fmt.Errorf("%s (%d bytes) does not fit on %s (%d bytes)", opts.Blob, st.Size(), opts.Device, size)
	}
	total := st.Size()
	if err := copyFileAt(dev, opts.Blob, 0, func(done int64) { progress(opts.Progress, done, total) }); err != nil {
		return err
	}
	return rawSyncError(opts.Device, dev.Sync())
}

// writePlan writes the GPT (primary + backup) and every partition image at
// its placed offset.
func writePlan(opts WriterOptions) error {
	plan, err := LoadPlan(opts.BundleDir)
	if err != nil {
		return err
	}
	dev, err := os.OpenFile(opts.Device, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("opening %s (requires root): %w", opts.Device, err)
	}
	defer dev.Close()

	size, err := blockDeviceSize(dev)
	if err != nil {
		return fmt.Errorf("sizing %s: %w", opts.Device, err)
	}
	gpt, err := BuildGPT(plan, size)
	if err != nil {
		return err
	}

	// Total for progress: partition file bytes (the GPT is noise).
	var total, done int64
	for _, p := range plan.Partitions {
		if p.File == "" {
			continue
		}
		st, err := os.Stat(filepath.Join(opts.BundleDir, p.File))
		if err != nil {
			return fmt.Errorf("partition %s: %w", p.Name, err)
		}
		if st.Size() > p.SizeSectors*sectorSize {
			return fmt.Errorf("partition %s: image %s (%d bytes) exceeds partition size (%d bytes)",
				p.Name, p.File, st.Size(), p.SizeSectors*sectorSize)
		}
		total += st.Size()
	}

	// Partition images first, GPT last: the disk only presents a valid
	// partition table once all content is in place, so an interrupted write
	// leaves an obviously-blank disk instead of a plausible-but-torn one.
	for _, p := range plan.Partitions {
		if p.File == "" {
			continue
		}
		err := copyFileAt(dev, filepath.Join(opts.BundleDir, p.File), p.StartSector*sectorSize,
			func(n int64) { progress(opts.Progress, done+n, total) })
		if err != nil {
			return fmt.Errorf("writing partition %s: %w", p.Name, err)
		}
		st, _ := os.Stat(filepath.Join(opts.BundleDir, p.File))
		done += st.Size()
	}
	if _, err := dev.WriteAt(gpt.Primary, 0); err != nil {
		return fmt.Errorf("writing primary GPT: %w", err)
	}
	if _, err := dev.WriteAt(gpt.Backup, gpt.BackupOffset); err != nil {
		return fmt.Errorf("writing backup GPT: %w", err)
	}
	return rawSyncError(opts.Device, dev.Sync())
}

// dumpDevice copies the first DumpBytes of the device into DumpTo (used to
// read the flashpkg filesystem back for the status report). The output file
// is made world-readable so the unprivileged parent can parse it.
func dumpDevice(opts WriterOptions) error {
	dev, err := os.Open(opts.Device)
	if err != nil {
		return fmt.Errorf("opening %s (requires root): %w", opts.Device, err)
	}
	defer dev.Close()
	out, err := os.OpenFile(opts.DumpTo, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.CopyN(out, dev, opts.DumpBytes); err != nil {
		return fmt.Errorf("reading %s: %w", opts.Device, err)
	}
	return out.Sync()
}

// copyFileAt writes path's contents to dev starting at offset, in
// sector-padded chunks (raw devices require whole-sector writes; the zero
// padding stays inside the partition, whose size is a sector multiple).
func copyFileAt(dev *os.File, path string, offset int64, onProgress func(int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, writeChunk)
	var done int64
	for {
		n, readErr := io.ReadFull(f, buf)
		if n > 0 {
			padded := (n + sectorSize - 1) / sectorSize * sectorSize
			for i := n; i < padded; i++ {
				buf[i] = 0
			}
			if _, err := dev.WriteAt(buf[:padded], offset+done); err != nil {
				return err
			}
			done += int64(n)
			if onProgress != nil {
				onProgress(done)
			}
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func progress(w io.Writer, done, total int64) {
	if w != nil {
		fmt.Fprintf(w, "PROGRESS %d %d\n", done, total)
	}
}
