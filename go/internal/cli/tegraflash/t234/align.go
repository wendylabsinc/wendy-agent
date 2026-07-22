//go:build darwin || linux || windows

package t234

import "os"

// alignedDevice adapts a raw block device to the sub-sector reads and writes
// the pure-Go filesystem writers (go-diskfs ext4/fat32 Create) make. Raw disk
// handles accept only whole-sector, sector-aligned transfers — Windows
// disk-class drivers and macOS raw character devices reject anything else —
// so unaligned access is converted to sector-granular read-modify-write here.
// Aligned transfers pass straight through.
type alignedDevice struct {
	*os.File
}

func (d alignedDevice) ReadAt(p []byte, off int64) (int, error) {
	if off%sectorSize == 0 && int64(len(p))%sectorSize == 0 {
		return d.File.ReadAt(p, off)
	}
	span, start, err := d.readSpan(int64(len(p)), off)
	if err != nil {
		return 0, err
	}
	copy(p, span[off-start:])
	return len(p), nil
}

func (d alignedDevice) WriteAt(p []byte, off int64) (int, error) {
	if off%sectorSize == 0 && int64(len(p))%sectorSize == 0 {
		return d.File.WriteAt(p, off)
	}
	span, start, err := d.readSpan(int64(len(p)), off)
	if err != nil {
		return 0, err
	}
	copy(span[off-start:], p)
	if _, err := d.File.WriteAt(span, start); err != nil {
		return 0, err
	}
	return len(p), nil
}

// readSpan reads the smallest sector-aligned span covering [off, off+n).
func (d alignedDevice) readSpan(n, off int64) ([]byte, int64, error) {
	start := off - off%sectorSize
	end := (off + n + sectorSize - 1) / sectorSize * sectorSize
	span := make([]byte, end-start)
	if _, err := d.File.ReadAt(span, start); err != nil {
		return nil, 0, err
	}
	return span, start, nil
}
