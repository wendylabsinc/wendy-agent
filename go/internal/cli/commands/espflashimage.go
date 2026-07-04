//go:build darwin || linux || windows

package commands

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
)

const (
	espPartTableOffset = 0x8000
	espPartTableMaxLen = 0xC00 // 3 KB — max table data before MD5/terminator
	espPartEntrySize   = 32
	espPartMagic       = uint16(0x50AA)
	espPartMagicMD5    = uint16(0xEBEB)
)

type espPartEntry struct {
	Type    uint8
	SubType uint8
	Offset  uint32
	Size    uint32
	Label   string
}

// EspFlashImage holds a full ESP32 flash binary and exposes partition-level writes.
type EspFlashImage struct {
	data []byte
}

// LoadEspFlashImage reads a flash image from path.
func LoadEspFlashImage(path string) (*EspFlashImage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading flash image: %w", err)
	}
	return &EspFlashImage{data: data}, nil
}

// NewEspFlashImage wraps an in-memory flash binary. The slice is copied.
func NewEspFlashImage(data []byte) *EspFlashImage {
	cp := make([]byte, len(data))
	copy(cp, data)
	return &EspFlashImage{data: cp}
}

// Bytes returns the full flash image contents.
func (img *EspFlashImage) Bytes() []byte {
	return img.data
}

// SetPartition overwrites the named partition's region with data, padding the
// remainder with 0xFF (the erased-flash value). Returns an error if the
// partition is not found in the embedded table or data exceeds its size.
func (img *EspFlashImage) SetPartition(name string, data []byte) error {
	entry, err := img.findPartition(name)
	if err != nil {
		return err
	}
	if uint32(len(data)) > entry.Size {
		return fmt.Errorf("data (%d bytes) exceeds partition %q size (%d bytes)", len(data), name, entry.Size)
	}
	end := entry.Offset + entry.Size
	if int(end) > len(img.data) {
		extended := make([]byte, int(end))
		copy(extended, img.data)
		for i := len(img.data); i < len(extended); i++ {
			extended[i] = 0xFF
		}
		img.data = extended
	}
	dest := img.data[entry.Offset:end]
	copy(dest, data)
	for i := len(data); i < len(dest); i++ {
		dest[i] = 0xFF
	}
	return nil
}

// findPartition scans the ESP32 partition table embedded at offset 0x8000.
func (img *EspFlashImage) findPartition(name string) (*espPartEntry, error) {
	tableEnd := min(espPartTableOffset+espPartTableMaxLen, len(img.data))
	for off := espPartTableOffset; off+espPartEntrySize <= tableEnd; off += espPartEntrySize {
		raw := img.data[off : off+espPartEntrySize]
		magic := binary.LittleEndian.Uint16(raw)
		if magic == espPartMagicMD5 {
			continue
		}
		if magic != espPartMagic {
			break // 0xFFFF end-of-table or corrupt data
		}
		label := strings.TrimRight(string(raw[12:28]), "\x00")
		if label == name {
			return &espPartEntry{
				Type:    raw[2],
				SubType: raw[3],
				Offset:  binary.LittleEndian.Uint32(raw[4:]),
				Size:    binary.LittleEndian.Uint32(raw[8:]),
				Label:   label,
			}, nil
		}
	}
	return nil, fmt.Errorf("partition %q not found in flash image", name)
}
