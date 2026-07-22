//go:build darwin || linux || windows

package t234

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	backendfile "github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/diskfs/go-diskfs/filesystem/fat32"
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
	Device       string // raw block device, e.g. /dev/rdisk4
	Blob         string // write this file at offset 0
	WritePlan    bool   // write the schema-v2 GPT + partition images
	LayoutPath   string // schema-v2 initrd-flash.xml
	ImagesDir    string // directory containing layout-referenced partition images
	RootfsDevice string
	DumpTo       string // read DumpBytes from the device into this file
	DumpBytes    int64
	Progress     io.Writer // "PROGRESS <bytes> <total>" lines; may be nil
}

const writeChunk = 4 << 20 // 4 MiB write buffer (padded to full sectors)

// Volume-label byte limits for the pure-Go blank-filesystem writers. LoadXMLPlan
// permits GPT partition names up to 36 UTF-16 units, but ext4 stores a 16-byte
// label and FAT32 an 11-byte one; a longer name would fail Create and abort the
// flash. The label is cosmetic — the GPT partition name carries the identity —
// so clamping it is safe.
const (
	ext4LabelMax  = 16
	fat32LabelMax = 11
)

// clampLabel truncates name to at most max bytes, dropping a trailing partial
// UTF-8 sequence so the label stays valid.
func clampLabel(name string, max int) string {
	if len(name) <= max {
		return name
	}
	b := name[:max]
	for len(b) > 0 && !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}

// RunWriter executes the selected operation. It requires root for raw block
// device access on macOS and Linux, and Administrator on Windows.
func RunWriter(opts WriterOptions) error {
	operations := 0
	if opts.Blob != "" {
		operations++
	}
	if opts.WritePlan {
		operations++
	}
	if opts.DumpTo != "" {
		operations++
	}
	if operations != 1 {
		return fmt.Errorf("exactly one of --blob, --write-plan, or --dump is required")
	}
	switch {
	case opts.Blob != "":
		return writeBlob(opts)
	case opts.WritePlan:
		return writePlan(opts)
	case opts.DumpTo != "":
		return dumpDevice(opts)
	}
	return nil
}

// writeBlob writes a single file verbatim at offset 0 (the flashpkg LUN).
func writeBlob(opts WriterOptions) error {
	// O_RDWR (not O_WRONLY): prepareRawTarget's SET_DISK_ATTRIBUTES IOCTL needs
	// read+write access on the handle, and copyFileAt only ever writes.
	dev, err := os.OpenFile(opts.Device, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening %s (%s): %w", opts.Device, devOpenPrivilege, err)
	}
	defer dev.Close()

	// Same offline guard as writePlan: stop Windows auto-mounting a volume on
	// the target LUN and denying the raw write. No-op off Windows.
	if err := prepareRawTarget(dev); err != nil {
		return err
	}

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
	if opts.LayoutPath == "" || opts.ImagesDir == "" || opts.RootfsDevice == "" {
		return fmt.Errorf("--layout, --images, and --rootfs-device are required with --write-plan")
	}
	plan, err := LoadXMLPlan(opts.LayoutPath, opts.ImagesDir, opts.RootfsDevice)
	if err != nil {
		return err
	}
	dev, err := os.OpenFile(opts.Device, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening %s (%s): %w", opts.Device, devOpenPrivilege, err)
	}
	defer dev.Close()

	// Take the disk offline (Windows) so a volume the host mounts on the target
	// — e.g. a stale ESP from a prior flash — cannot deny the raw write.
	if err := prepareRawTarget(dev); err != nil {
		return err
	}

	size, err := blockDeviceSize(dev)
	if err != nil {
		return fmt.Errorf("sizing %s: %w", opts.Device, err)
	}
	resolved, err := plan.ResolveForDevice(size / sectorSize)
	if err != nil {
		return err
	}
	gpt, err := BuildGPT(resolved, size)
	if err != nil {
		return err
	}

	// Total for progress: partition file bytes (the GPT is noise).
	var total, done int64
	for _, p := range resolved.Partitions {
		if p.File == "" {
			continue
		}
		st, err := os.Stat(filepath.Join(opts.ImagesDir, p.File))
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
	for _, p := range resolved.Partitions {
		if p.File == "" {
			continue
		}
		err := copyFileAt(dev, filepath.Join(opts.ImagesDir, p.File), p.StartSector*sectorSize,
			func(n int64) { progress(opts.Progress, done+n, total) })
		if err != nil {
			return fmt.Errorf("writing partition %s: %w", p.Name, err)
		}
		st, _ := os.Stat(filepath.Join(opts.ImagesDir, p.File))
		done += st.Size()
	}
	// Create explicitly-requested blank filesystems directly at their partition
	// offsets. This uses pure Go filesystem writers and never shells out to mkfs.
	// It happens before the GPT is committed, so an interruption cannot leave a
	// plausible partition table pointing at a half-created filesystem. The
	// writers make sub-sector accesses (e.g. 256-byte ext4 inode-table writes)
	// that raw disk handles reject; alignedDevice converts those to
	// sector-granular read-modify-write.
	backend := backendfile.New(alignedDevice{dev}, false)
	for _, p := range resolved.Partitions {
		if p.File != "" || p.FsType == "" || p.FsType == "basic" {
			continue
		}
		partSize := p.SizeSectors * sectorSize
		partOffset := p.StartSector * sectorSize
		var closeFS interface{ Close() error }
		switch p.FsType {
		case "ext4":
			closeFS, err = ext4.Create(backend, partSize, partOffset, sectorSize, &ext4.Params{VolumeName: clampLabel(p.Name, ext4LabelMax)})
		case "fat32", "vfat":
			closeFS, err = fat32.Create(backend, partSize, partOffset, sectorSize, clampLabel(p.Name, fat32LabelMax), false)
		}
		if err != nil {
			return fmt.Errorf("creating %s filesystem on partition %s: %w", p.FsType, p.Name, err)
		}
		if closeFS != nil {
			if err := closeFS.Close(); err != nil {
				return fmt.Errorf("closing filesystem on partition %s: %w", p.Name, err)
			}
		}
	}
	if _, err := dev.WriteAt(gpt.Primary, 0); err != nil {
		return fmt.Errorf("writing primary GPT: %w", err)
	}
	if _, err := dev.WriteAt(gpt.Backup, gpt.BackupOffset); err != nil {
		return fmt.Errorf("writing backup GPT: %w", err)
	}
	if err := rawSyncError(opts.Device, dev.Sync()); err != nil {
		return err
	}
	if err := VerifyGPT(dev, resolved, size); err != nil {
		return fmt.Errorf("verifying written GPT: %w", err)
	}
	return nil
}

// dumpDevice copies the first DumpBytes of the device into DumpTo (used to
// read the flashpkg filesystem back for the status report). The output file
// is made world-readable so the unprivileged parent can parse it.
func dumpDevice(opts WriterOptions) error {
	if opts.DumpBytes <= 0 {
		return fmt.Errorf("--bytes must be positive with --dump")
	}
	dev, err := os.Open(opts.Device)
	if err != nil {
		return fmt.Errorf("opening %s (%s): %w", opts.Device, devOpenPrivilege, err)
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
