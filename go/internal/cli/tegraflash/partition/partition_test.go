package partition

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

func TestPartitionTypeID(t *testing.T) {
	cases := map[string]uint32{
		"mb1_bootloader":        0x14,
		"psc_bl1":               0x30,
		"mb2_applet":            0x2c,
		"bootloader":            0x02,
		"mem_boot_config_table": 0x27,
	}
	for name, want := range cases {
		got, ok := PartitionTypeID(name)
		if !ok || got != want {
			t.Errorf("PartitionTypeID(%q) = 0x%x,%v want 0x%x", name, got, ok, want)
		}
	}
	if _, ok := PartitionTypeID("not_a_type"); ok {
		t.Error("unknown type should return ok=false")
	}
}

func TestDeviceTypeID(t *testing.T) {
	cases := map[string]uint32{
		"sdmmc_boot": 0,
		"sdmmc_user": 1,
		"spi":        3,
		"ufs":        7,
		"ufs_user":   8,
		"rcm":        10,
		"nvme":       12,
	}
	for name, want := range cases {
		got, ok := DeviceTypeID(name)
		if !ok || got != want {
			t.Errorf("DeviceTypeID(%q) = %d,%v want %d", name, got, ok, want)
		}
	}
	if _, ok := DeviceTypeID("not_a_device"); ok {
		t.Error("unknown device type should return ok=false")
	}
}

func TestFilesystemTypeID(t *testing.T) {
	cases := map[string]uint32{
		"basic":    1,
		"enhanced": 2,
		"ext2":     3,
		"yaffs2":   4,
		"ext3":     5,
		"ext4":     6,
		"qnx":      7,
	}
	for name, want := range cases {
		got, ok := FilesystemTypeID(name)
		if !ok || got != want {
			t.Errorf("FilesystemTypeID(%q) = %d,%v want %d", name, got, ok, want)
		}
	}
}

func TestSerializeMatchesGolden(t *testing.T) {
	xmlData, err := os.ReadFile("../testdata/golden/rcmboot-flash.xml")
	if err != nil {
		t.Skip("golden input not present")
	}
	layout, err := Parse(xmlData)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, err := layout.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	want, err := os.ReadFile("../testdata/golden/pt.bin")
	if err != nil {
		t.Fatalf("golden pt.bin: %v", err)
	}
	// Pointer fields and random GUIDs differ; compare structurally.
	assertPartitionTableEqual(t, got, want)
}

// assertPartitionTableEqual compares two serialized partition-table buffers for
// structural equality after zeroing:
//   - the pointer placeholder at header+0x08,
//   - the partition-array pointer at each device record +0x1c,
//   - the name pointer at each partition record +0x00,
//   - the filename pointer at each partition record +0x78,
//   - the unique_guid at each partition record +0x68 (16 bytes).
//
// These fields differ between runs (stale process pointers and random GUIDs).
func assertPartitionTableEqual(t *testing.T, got, want []byte) {
	t.Helper()
	if len(got) < 12 || len(want) < 12 {
		t.Fatalf("buffers too short: got=%d want=%d", len(got), len(want))
	}

	// Read structural counts from the header (same in both buffers after normalization).
	numDevices := int(binary.LittleEndian.Uint32(want[4:8]))
	if len(want) < 12+numDevices*0x20 {
		t.Fatalf("want buffer too short for %d devices", numDevices)
	}

	// Count total partitions across all devices.
	totalParts := 0
	for i := 0; i < numDevices; i++ {
		off := 0x0c + i*0x20
		numParts := int(binary.LittleEndian.Uint32(want[off+0x08 : off+0x0c]))
		totalParts += numParts
	}

	zero := func(buf []byte, off, size int) {
		if off+size <= len(buf) {
			for i := off; i < off+size; i++ {
				buf[i] = 0
			}
		}
	}

	// Normalize both buffers in place.
	for _, buf := range [][]byte{got, want} {
		if len(buf) < 12 {
			continue
		}
		// Header pointer at +0x08.
		zero(buf, 0x08, 4)

		// Per-device layout: for each device, zero device+0x1c, then for each
		// partition in that device zero the name ptr, filename ptr, and unique_guid.
		// The binary lays out records as: all device records, then PER DEVICE:
		// partition records, then name/filename strings.
		// We only need to zero within the device/partition records.
		for i := 0; i < numDevices; i++ {
			devOff := 0x0c + i*0x20
			// Device partition-array pointer.
			zero(buf, devOff+0x1c, 4)
		}

		// Partition records follow device records.  They are laid out per device
		// (each device's partition records immediately precede that device's strings).
		cursor := 0x0c + numDevices*0x20
		for i := 0; i < numDevices; i++ {
			devOff := 0x0c + i*0x20
			numParts := int(binary.LittleEndian.Uint32(buf[devOff+0x08 : devOff+0x0c]))
			for j := 0; j < numParts; j++ {
				pOff := cursor + j*0x80
				zero(buf, pOff+0x00, 4)  // name pointer
				zero(buf, pOff+0x78, 4)  // filename pointer
				zero(buf, pOff+0x68, 16) // unique_guid
			}
			// Advance cursor past partition records and strings for this device.
			cursor += numParts * 0x80
			// Skip strings for this device: read them to find end of string region.
			for j := 0; j < numParts; j++ {
				// name string
				for cursor < len(buf) && buf[cursor] != 0 {
					cursor++
				}
				cursor++ // consume NUL
				// filename string (always present)
				for cursor < len(buf) && buf[cursor] != 0 {
					cursor++
				}
				cursor++ // consume NUL
			}
		}
	}

	if !bytes.Equal(got, want) {
		// Find first difference.
		first := -1
		maxLen := len(got)
		if len(want) < maxLen {
			maxLen = len(want)
		}
		for i := 0; i < maxLen; i++ {
			if got[i] != want[i] {
				first = i
				break
			}
		}
		if first == -1 {
			t.Errorf("buffers have same content but different lengths: got=%d want=%d", len(got), len(want))
		} else {
			t.Errorf("first difference at byte 0x%x: got=0x%02x want=0x%02x (got len=%d, want len=%d)",
				first, got[first], want[first], len(got), len(want))
		}
	}
}
