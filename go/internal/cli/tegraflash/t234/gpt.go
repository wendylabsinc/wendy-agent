package t234

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"unicode/utf16"
)

// GPT geometry constants (512-byte sectors, 128 entries of 128 bytes).
const (
	gptEntrySize     = 128
	gptNumEntries    = 128
	gptEntrySectors  = gptEntrySize * gptNumEntries / sectorSize // 32
	gptPrimarySize   = (2 + gptEntrySectors) * sectorSize        // MBR + header + entries
	gptBackupSectors = gptEntrySectors + 1                       // entries + header
)

// Partition type GUIDs. The flash layout gives an explicit GUID for some
// partitions (ESP, config, data); everything else defaults to Microsoft basic
// data — the same default as sgdisk code 0700, which the bundle's make-sdcard
// uses for unspecified types.
const defaultTypeGUID = "EBD0A0A2-B9E5-4433-87C0-68B6B72699C7"

// GPT is a built partition table, ready to write to the device.
type GPT struct {
	Primary       []byte // sectors 0 .. 33 (protective MBR, header, entries)
	Backup        []byte // last 33 sectors (entries, then header)
	BackupOffset  int64  // byte offset of Backup on the device
	DeviceSectors int64
}

// BuildGPT lays out the plan's partitions on a device of deviceSize bytes and
// returns the primary and backup GPT structures. The partition placement must
// already be computed (LoadPlan does this).
func BuildGPT(p *Plan, deviceSize int64) (*GPT, error) {
	if deviceSize%sectorSize != 0 {
		return nil, fmt.Errorf("device size %d is not a multiple of %d", deviceSize, sectorSize)
	}
	sectors := deviceSize / sectorSize
	if min := p.MinDeviceSectors(); sectors < min {
		return nil, fmt.Errorf("device too small: %d sectors, plan needs at least %d", sectors, min)
	}

	diskGUID, err := randomGUID()
	if err != nil {
		return nil, err
	}

	// Partition entry array (shared verbatim by primary and backup).
	entries := make([]byte, gptEntrySize*gptNumEntries)
	for _, part := range p.Partitions {
		if part.Number < 1 || part.Number > gptNumEntries {
			return nil, fmt.Errorf("partition number %d out of range", part.Number)
		}
		e := entries[(part.Number-1)*gptEntrySize : part.Number*gptEntrySize]
		typeGUID := part.TypeGuid
		if typeGUID == "" {
			typeGUID = defaultTypeGUID
		}
		if err := putGUID(e[0:16], typeGUID); err != nil {
			return nil, fmt.Errorf("partition %s: %w", part.Name, err)
		}
		unique, err := randomGUID()
		if err != nil {
			return nil, err
		}
		copy(e[16:32], unique)
		binary.LittleEndian.PutUint64(e[32:40], uint64(part.StartSector))
		binary.LittleEndian.PutUint64(e[40:48], uint64(part.StartSector+part.SizeSectors-1))
		// attributes (48:56) zero
		name := utf16.Encode([]rune(part.Name))
		if len(name) > 36 {
			return nil, fmt.Errorf("partition name %q longer than 36 UTF-16 units", part.Name)
		}
		for i, u := range name {
			binary.LittleEndian.PutUint16(e[56+2*i:58+2*i], u)
		}
	}
	entriesCRC := crc32.ChecksumIEEE(entries)

	header := func(currentLBA, backupLBA, entriesLBA int64) []byte {
		h := make([]byte, sectorSize)
		copy(h[0:8], "EFI PART")
		binary.LittleEndian.PutUint32(h[8:12], 0x00010000) // revision 1.0
		binary.LittleEndian.PutUint32(h[12:16], 92)        // header size
		binary.LittleEndian.PutUint64(h[24:32], uint64(currentLBA))
		binary.LittleEndian.PutUint64(h[32:40], uint64(backupLBA))
		binary.LittleEndian.PutUint64(h[40:48], uint64(2+gptEntrySectors))          // first usable
		binary.LittleEndian.PutUint64(h[48:56], uint64(sectors-1-gptBackupSectors)) // last usable
		copy(h[56:72], diskGUID)                                                    //
		binary.LittleEndian.PutUint64(h[72:80], uint64(entriesLBA))                 //
		binary.LittleEndian.PutUint32(h[80:84], gptNumEntries)                      //
		binary.LittleEndian.PutUint32(h[84:88], gptEntrySize)                       //
		binary.LittleEndian.PutUint32(h[88:92], entriesCRC)                         //
		binary.LittleEndian.PutUint32(h[16:20], crc32.ChecksumIEEE(h[0:92]))        // header CRC
		return h
	}

	var primary bytes.Buffer
	primary.Write(protectiveMBR(sectors))
	primary.Write(header(1, sectors-1, 2))
	primary.Write(entries)

	var backup bytes.Buffer
	backup.Write(entries)
	backup.Write(header(sectors-1, 1, sectors-1-gptEntrySectors))

	return &GPT{
		Primary:       primary.Bytes(),
		Backup:        backup.Bytes(),
		BackupOffset:  (sectors - gptBackupSectors) * sectorSize,
		DeviceSectors: sectors,
	}, nil
}

// protectiveMBR builds the LBA-0 protective MBR: one 0xEE partition covering
// the whole disk (capped at 2^32-1 sectors).
func protectiveMBR(sectors int64) []byte {
	mbr := make([]byte, sectorSize)
	size := sectors - 1
	if size > 0xFFFFFFFF {
		size = 0xFFFFFFFF
	}
	p := mbr[446:462]
	p[1], p[2], p[3] = 0x00, 0x02, 0x00 // first CHS
	p[4] = 0xEE                         // GPT protective
	p[5], p[6], p[7] = 0xFF, 0xFF, 0xFF // last CHS
	binary.LittleEndian.PutUint32(p[8:12], 1)
	binary.LittleEndian.PutUint32(p[12:16], uint32(size))
	mbr[510], mbr[511] = 0x55, 0xAA
	return mbr
}

// putGUID writes a textual GUID in GPT on-disk (mixed-endian) form.
func putGUID(dst []byte, guid string) error {
	var b [16]byte
	n, err := fmt.Sscanf(guid,
		"%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		&b[0], &b[1], &b[2], &b[3], &b[4], &b[5], &b[6], &b[7],
		&b[8], &b[9], &b[10], &b[11], &b[12], &b[13], &b[14], &b[15])
	if err != nil || n != 16 {
		return fmt.Errorf("invalid GUID %q", guid)
	}
	// First three fields little-endian, rest big-endian.
	dst[0], dst[1], dst[2], dst[3] = b[3], b[2], b[1], b[0]
	dst[4], dst[5] = b[5], b[4]
	dst[6], dst[7] = b[7], b[6]
	copy(dst[8:16], b[8:16])
	return nil
}

// randomGUID returns 16 on-disk GUID bytes for a fresh RFC-4122 v4 GUID.
func randomGUID() ([]byte, error) {
	var g [16]byte
	if _, err := rand.Read(g[:]); err != nil {
		return nil, fmt.Errorf("generating GUID: %w", err)
	}
	// Set version 4 / variant bits in textual (big-endian field) terms; the
	// buffer is already in on-disk order for random data, but keep the
	// version nibble where tools expect it after byte-swapping.
	g[7] = g[7]&0x0F | 0x40 // time_hi_and_version high byte (on-disk offset 7)
	g[8] = g[8]&0x3F | 0x80 // clock_seq_hi variant
	return g[:], nil
}
