package t234

import (
	"encoding/binary"
	"hash/crc32"
	"testing"
	"unicode/utf16"
)

// agxOrinEMMCSize is the 64 GB module's capacity (EMMC_SIZE in the machine
// conf): 124321792 sectors.
const agxOrinEMMCSize = int64(63652757504)

func buildTestGPT(t *testing.T) (*Plan, *GPT) {
	t.Helper()
	p := testPlan()
	if err := p.place(); err != nil {
		t.Fatal(err)
	}
	gpt, err := BuildGPT(p, agxOrinEMMCSize)
	if err != nil {
		t.Fatal(err)
	}
	return p, gpt
}

func TestBuildGPTStructure(t *testing.T) {
	p, gpt := buildTestGPT(t)

	if len(gpt.Primary) != gptPrimarySize {
		t.Fatalf("primary size %d, want %d", len(gpt.Primary), gptPrimarySize)
	}
	if len(gpt.Backup) != gptBackupSectors*sectorSize {
		t.Fatalf("backup size %d", len(gpt.Backup))
	}
	sectors := agxOrinEMMCSize / sectorSize
	if gpt.BackupOffset != (sectors-gptBackupSectors)*sectorSize {
		t.Fatalf("backup offset %d", gpt.BackupOffset)
	}

	// Protective MBR.
	if gpt.Primary[510] != 0x55 || gpt.Primary[511] != 0xAA {
		t.Error("missing MBR signature")
	}
	if gpt.Primary[446+4] != 0xEE {
		t.Error("protective partition type is not 0xEE")
	}

	// Primary header.
	hdr := gpt.Primary[sectorSize : 2*sectorSize]
	if string(hdr[0:8]) != "EFI PART" {
		t.Fatal("missing GPT signature")
	}
	if got := binary.LittleEndian.Uint32(hdr[16:20]); got != headerCRC(hdr) {
		t.Errorf("primary header CRC mismatch: stored %08x, computed %08x", got, headerCRC(hdr))
	}
	if got := binary.LittleEndian.Uint64(hdr[32:40]); got != uint64(sectors-1) {
		t.Errorf("backup LBA = %d, want %d", got, sectors-1)
	}
	if got := binary.LittleEndian.Uint64(hdr[48:56]); got != uint64(sectors-1-gptBackupSectors) {
		t.Errorf("last usable = %d", got)
	}

	// Entries CRC covers the entry array, shared by both copies.
	entries := gpt.Primary[2*sectorSize:]
	if got := binary.LittleEndian.Uint32(hdr[88:92]); got != crc32.ChecksumIEEE(entries) {
		t.Error("entries CRC mismatch in primary header")
	}
	backupEntries := gpt.Backup[:gptEntrySectors*sectorSize]
	if string(entries) != string(backupEntries) {
		t.Error("backup entry array differs from primary")
	}

	// Backup header mirrors the primary with swapped roles.
	bhdr := gpt.Backup[gptEntrySectors*sectorSize:]
	if got := binary.LittleEndian.Uint32(bhdr[16:20]); got != headerCRC(bhdr) {
		t.Error("backup header CRC mismatch")
	}
	if got := binary.LittleEndian.Uint64(bhdr[24:32]); got != uint64(sectors-1) {
		t.Errorf("backup current LBA = %d", got)
	}
	if got := binary.LittleEndian.Uint64(bhdr[72:80]); got != uint64(sectors-1-gptEntrySectors) {
		t.Errorf("backup entries LBA = %d", got)
	}

	// Every partition entry: bounds and name round-trip.
	for _, part := range p.Partitions {
		e := entries[(part.Number-1)*gptEntrySize : part.Number*gptEntrySize]
		if got := binary.LittleEndian.Uint64(e[32:40]); got != uint64(part.StartSector) {
			t.Errorf("%s: first LBA %d, want %d", part.Name, got, part.StartSector)
		}
		if got := binary.LittleEndian.Uint64(e[40:48]); got != uint64(part.StartSector+part.SizeSectors-1) {
			t.Errorf("%s: last LBA %d", part.Name, got)
		}
		var units []uint16
		for i := 56; i < 128; i += 2 {
			u := binary.LittleEndian.Uint16(e[i : i+2])
			if u == 0 {
				break
			}
			units = append(units, u)
		}
		if got := string(utf16.Decode(units)); got != part.Name {
			t.Errorf("entry name %q, want %q", got, part.Name)
		}
	}

	// Unused entries stay zero.
	for i := 17 * gptEntrySize; i < len(entries); i++ {
		if entries[i] != 0 {
			t.Fatalf("unused entry space is non-zero at %d", i)
		}
	}
}

func TestBuildGPTTypeGUIDs(t *testing.T) {
	p, gpt := buildTestGPT(t)
	entries := gpt.Primary[2*sectorSize:]

	// The ESP's explicit type GUID (C12A7328-...) must be encoded
	// mixed-endian: first field little-endian.
	var esp Partition
	for _, part := range p.Partitions {
		if part.Name == "esp" {
			esp = part
		}
	}
	e := entries[(esp.Number-1)*gptEntrySize:]
	want := []byte{0x28, 0x73, 0x2A, 0xC1, 0x1F, 0xF8, 0xD2, 0x11, 0xBA, 0x4B, 0x00, 0xA0, 0xC9, 0x3E, 0xC9, 0x3B}
	for i, b := range want {
		if e[i] != b {
			t.Fatalf("ESP type GUID byte %d = %02x, want %02x", i, e[i], b)
		}
	}
}

func TestBuildGPTTooSmall(t *testing.T) {
	p := testPlan()
	if err := p.place(); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildGPT(p, 1<<30); err == nil {
		t.Fatal("expected too-small error")
	}
}

func headerCRC(hdr []byte) uint32 {
	tmp := make([]byte, 92)
	copy(tmp, hdr[:92])
	tmp[16], tmp[17], tmp[18], tmp[19] = 0, 0, 0, 0
	return crc32.ChecksumIEEE(tmp)
}
