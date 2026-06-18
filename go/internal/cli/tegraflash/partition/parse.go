package partition

import (
	"encoding/xml"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
)

// xmlLayout mirrors the structure of the partition_layout XML document.
type xmlLayout struct {
	XMLName xml.Name    `xml:"partition_layout"`
	Version string      `xml:"version,attr"`
	Devices []xmlDevice `xml:"device"`
}

type xmlDevice struct {
	Type        string         `xml:"type,attr"`
	Instance    string         `xml:"instance,attr"`
	SectorSize  string         `xml:"sector_size,attr"`
	NumSectors  string         `xml:"num_sectors,attr"`
	Erase       string         `xml:"erase,attr"`
	Partitions  []xmlPartition `xml:"partition"`
}

type xmlPartition struct {
	Name          string `xml:"name,attr"`
	Type          string `xml:"type,attr"`
	OEMSign       string `xml:"oem_sign,attr"`
	AuthGroup     string `xml:"authentication_group,attr"`
	CompAlgo      string `xml:"comp_algo,attr"`
	RollbackLevel string `xml:"rollback_level,attr"`

	ID                   string `xml:"id"`
	AllocationPolicy     string `xml:"allocation_policy"`
	FilesystemType       string `xml:"filesystem_type"`
	Size                 string `xml:"size"`
	FileSystemAttribute  string `xml:"file_system_attribute"`
	AllocationAttribute  string `xml:"allocation_attribute"`
	PercentReserved      string `xml:"percent_reserved"`
	AlignBoundary        string `xml:"align_boundary"`
	EraseSize            string `xml:"erase_size"`
	Filename             string `xml:"filename"`
	StartLocation        string `xml:"start_location"`
	PartitionTypeGUID    string `xml:"partition_type_guid"`
	UniqueGUID           string `xml:"unique_guid"`
}

// partitionTypesSkipID contains the type IDs that are exempt from the GPT
// partition-ID range check and uniqueness check.
var partitionTypesSkipID = map[uint32]bool{
	5:    true, // master_boot_record
	7:    true, // primary_gpt
	8:    true, // secondary_gpt
	0x1a: true, // protective_master_boot_record
}

// defaultTypeGUID is the hardcoded default partition_type_guid used when the
// XML does not specify one.  Value observed in every partition record in the
// golden pt.bin at offset +0x58.
var defaultTypeGUID = [16]byte{
	0xa2, 0xa0, 0xd0, 0xeb, 0xe5, 0xb9, 0x33, 0x44,
	0x87, 0xc0, 0x68, 0xb6, 0xb7, 0x26, 0x99, 0xc7,
}

// parseVersion converts a version string of the form "MM.mm.bbbb" to the
// packed uint32: ((MM*10 + mm) * 1000) + bbbb.
func parseVersion(s string) (uint32, error) {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid version %q: expected MM.mm.bbbb", s)
	}
	mm, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid version major %q: %w", parts[0], err)
	}
	min, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid version minor %q: %w", parts[1], err)
	}
	build, err := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid version build %q: %w", parts[2], err)
	}
	packed := (mm*10+min)*1000 + build
	return uint32(packed), nil
}

// parseU64 parses a decimal or hex (0x-prefixed) uint64 string.
func parseU64(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strconv.ParseUint(s[2:], 16, 64)
	}
	return strconv.ParseUint(s, 10, 64)
}

// parseU32 parses a decimal or hex uint32 string.
func parseU32(s string) (uint32, error) {
	v, err := parseU64(s)
	return uint32(v), err
}

// parseBool returns true if s == "true" (case-insensitive).
func parseBool(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "true")
}

// parseGUID parses a GUID string of the form
// "AABBCCDD-EEFF-GGHH-IIJJ-KKLLMMNNOOPP" into a 16-byte array using the
// standard mixed-endian (GPT/UEFI) on-disk encoding used by
// NvTegraGuidStrToRec: group 1 (4 bytes) stored little-endian, group 2
// (2 bytes) little-endian, group 3 (2 bytes) little-endian, groups 4 and 5
// (8 bytes total) stored big-endian as written.
//
// Example: "0FC63DAF-8483-4772-8E79-3D69D8477DE4" encodes to
// af 3d c6 0f  83 84  72 47  8e 79 3d 69 d8 47 7d e4.
func parseGUID(s string) ([16]byte, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "-")
	if len(parts) != 5 {
		return [16]byte{}, fmt.Errorf("invalid GUID %q: expected 5 groups separated by '-'", s)
	}
	// Expected lengths of each group in hex digits.
	groupLen := [5]int{8, 4, 4, 4, 12}
	for i, p := range parts {
		if len(p) != groupLen[i] {
			return [16]byte{}, fmt.Errorf("invalid GUID %q: group %d has length %d, want %d", s, i+1, len(p), groupLen[i])
		}
	}

	parse := func(hex string) ([]byte, error) {
		out := make([]byte, len(hex)/2)
		for i := range out {
			v, err := strconv.ParseUint(hex[i*2:i*2+2], 16, 8)
			if err != nil {
				return nil, fmt.Errorf("invalid hex byte %q: %w", hex[i*2:i*2+2], err)
			}
			out[i] = byte(v)
		}
		return out, nil
	}

	var b [16]byte

	// Group 1: 4 bytes, little-endian (byte-reversed).
	g1, err := parse(parts[0])
	if err != nil {
		return [16]byte{}, fmt.Errorf("invalid GUID %q group 1: %w", s, err)
	}
	b[0], b[1], b[2], b[3] = g1[3], g1[2], g1[1], g1[0]

	// Group 2: 2 bytes, little-endian (byte-reversed).
	g2, err := parse(parts[1])
	if err != nil {
		return [16]byte{}, fmt.Errorf("invalid GUID %q group 2: %w", s, err)
	}
	b[4], b[5] = g2[1], g2[0]

	// Group 3: 2 bytes, little-endian (byte-reversed).
	g3, err := parse(parts[2])
	if err != nil {
		return [16]byte{}, fmt.Errorf("invalid GUID %q group 3: %w", s, err)
	}
	b[6], b[7] = g3[1], g3[0]

	// Groups 4 and 5: 4 + 6 bytes, big-endian (as written).
	g45, err := parse(parts[3] + parts[4])
	if err != nil {
		return [16]byte{}, fmt.Errorf("invalid GUID %q groups 4-5: %w", s, err)
	}
	copy(b[8:], g45)

	return b, nil
}

// randomUniqueGUID generates a random RFC-4122 version-4 UUID.
func randomUniqueGUID() [16]byte {
	var b [16]byte
	v := rand.Uint64()
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (i * 8))
	}
	v = rand.Uint64()
	for i := 0; i < 8; i++ {
		b[8+i] = byte(v >> (i * 8))
	}
	// Set version 4 (bits 12-15 of octet 6, which is b[6]).
	b[6] = (b[6] & 0x0f) | 0x40
	// Set variant bits (bits 6-7 of octet 8, which is b[8]).
	b[8] = (b[8] & 0x3f) | 0x80
	return b
}

// Parse parses a partition-layout XML document and returns a Layout.
func Parse(xmlData []byte) (*Layout, error) {
	var doc xmlLayout
	if err := xml.Unmarshal(xmlData, &doc); err != nil {
		return nil, fmt.Errorf("xml parse: %w", err)
	}

	version, err := parseVersion(doc.Version)
	if err != nil {
		return nil, err
	}

	layout := &Layout{Version: version}

	for di, xd := range doc.Devices {
		devTypeID, ok := DeviceTypeID(strings.TrimSpace(xd.Type))
		if !ok {
			return nil, fmt.Errorf("device %d: invalid device type %q", di, xd.Type)
		}
		instance, err := parseU32(xd.Instance)
		if err != nil {
			return nil, fmt.Errorf("device %d: invalid instance %q: %w", di, xd.Instance, err)
		}
		sectorSize, err := parseU32(xd.SectorSize)
		if err != nil {
			return nil, fmt.Errorf("device %d: invalid sector_size %q: %w", di, xd.SectorSize, err)
		}
		numSectors, err := parseU64(xd.NumSectors)
		if err != nil {
			return nil, fmt.Errorf("device %d: invalid num_sectors %q: %w", di, xd.NumSectors, err)
		}
		erase := true
		if strings.EqualFold(strings.TrimSpace(xd.Erase), "false") {
			erase = false
		}

		dev := Device{
			Type:       devTypeID,
			Instance:   instance,
			SectorSize: sectorSize,
			NumSectors: numSectors,
			Erase:      erase,
		}

		// Bitmap tracking assigned partition IDs within this device.
		// Two 64-bit words cover [1..128]: word index = (id-1)/64, bit = (id-1)%64.
		var idBitmap [2]uint64

		seqID := uint32(0) // next sequential ID to assign

		for pi, xp := range xd.Partitions {
			partTypeName := strings.TrimSpace(xp.Type)
			partTypeID, ok := PartitionTypeID(partTypeName)
			if !ok {
				return nil, fmt.Errorf("device %d partition %d: invalid partition type %q", di, pi, xp.Type)
			}

			fsTypeName := strings.TrimSpace(xp.FilesystemType)
			var fsTypeID uint32
			if fsTypeName != "" {
				fsTypeID, ok = FilesystemTypeID(fsTypeName)
				if !ok {
					return nil, fmt.Errorf("device %d partition %d: invalid filesystem_type %q", di, pi, xp.FilesystemType)
				}
			}

			// Parse numeric fields.
			size, err := parseU64(xp.Size)
			if err != nil {
				return nil, fmt.Errorf("device %d partition %d size: %w", di, pi, err)
			}
			startLocation, err := parseU64(xp.StartLocation)
			if err != nil {
				return nil, fmt.Errorf("device %d partition %d start_location: %w", di, pi, err)
			}
			fsAttr, err := parseU64(xp.FileSystemAttribute)
			if err != nil {
				return nil, fmt.Errorf("device %d partition %d file_system_attribute: %w", di, pi, err)
			}
			allocAttr, err := parseU32(xp.AllocationAttribute)
			if err != nil {
				return nil, fmt.Errorf("device %d partition %d allocation_attribute: %w", di, pi, err)
			}
			pctRes, err := parseU32(xp.PercentReserved)
			if err != nil {
				return nil, fmt.Errorf("device %d partition %d percent_reserved: %w", di, pi, err)
			}
			alignBoundary, err := parseU64(xp.AlignBoundary)
			if err != nil {
				return nil, fmt.Errorf("device %d partition %d align_boundary: %w", di, pi, err)
			}
			eraseSize, err := parseU32(xp.EraseSize)
			if err != nil {
				return nil, fmt.Errorf("device %d partition %d erase_size: %w", di, pi, err)
			}
			rollbackLevel, err := parseU32(xp.RollbackLevel)
			if err != nil {
				return nil, fmt.Errorf("device %d partition %d rollback_level: %w", di, pi, err)
			}

			// Partition ID: use <id> child if present, else sequential index+1.
			var partID uint32
			idStr := strings.TrimSpace(xp.ID)
			if idStr != "" {
				partID, err = parseU32(idStr)
				if err != nil {
					return nil, fmt.Errorf("device %d partition %d id: %w", di, pi, err)
				}
			} else {
				seqID++
				if !partitionTypesSkipID[partTypeID] {
					partID = seqID
				}
				// Skipped types get id=0 and do not increment the sequential counter
				// but they still count as a position; actually the counter already
				// incremented above. For skipped types, undo the increment and set id=0.
				if partitionTypesSkipID[partTypeID] {
					seqID--
					partID = 0
				}
			}

			// Range and uniqueness checks (skipped for exempt types).
			if !partitionTypesSkipID[partTypeID] && partID != 0 {
				if partID < 1 || partID > 128 {
					return nil, fmt.Errorf("device %d partition %d (%q): id %d out of range [1,128]",
						di, pi, xp.Name, partID)
				}
				word := (partID - 1) / 64
				bit := (partID - 1) % 64
				if idBitmap[word]&(1<<bit) != 0 {
					return nil, fmt.Errorf("device %d partition %d (%q): duplicate GPT id %d",
						di, pi, xp.Name, partID)
				}
				idBitmap[word] |= 1 << bit
			}

			// Type GUID.
			var typeGUID [16]byte
			if g := strings.TrimSpace(xp.PartitionTypeGUID); g != "" {
				typeGUID, err = parseGUID(g)
				if err != nil {
					return nil, fmt.Errorf("device %d partition %d type_guid: %w", di, pi, err)
				}
			} else {
				typeGUID = defaultTypeGUID
			}

			// Unique GUID.
			var uniqueGUID [16]byte
			if g := strings.TrimSpace(xp.UniqueGUID); g != "" {
				uniqueGUID, err = parseGUID(g)
				if err != nil {
					return nil, fmt.Errorf("device %d partition %d unique_guid: %w", di, pi, err)
				}
			} else {
				uniqueGUID = randomUniqueGUID()
			}

			// Filename: trim whitespace; an empty or whitespace-only value
			// becomes an empty string (serialized as a bare NUL).
			filename := strings.TrimSpace(xp.Filename)

			p := Partition{
				Name:                xp.Name,
				ID:                  partID,
				TypeID:              partTypeID,
				FSType:              fsTypeID,
				Size:                size,
				StartLocation:       startLocation,
				FileSystemAttribute: fsAttr,
				AlignBoundary:       alignBoundary,
				AllocationAttribute: allocAttr,
				PercentReserved:     pctRes,
				EraseSize:           eraseSize,
				RollbackLevel:       uint8(rollbackLevel),
				OEMSign: parseBool(xp.OEMSign),
				// The field at +0x49 is set by authentication_group="true" OR
				// by comp_algo="lz4".  The golden binary confirms comp_algo="lz4"
				// sets this byte; the RE doc incorrectly attributes it solely to
				// authentication_group.
				AuthGroup: parseBool(xp.AuthGroup) ||
					strings.EqualFold(strings.TrimSpace(xp.CompAlgo), "lz4"),
				TypeGUID:            typeGUID,
				UniqueGUID:          uniqueGUID,
				Filename:            filename,
			}
			dev.Partitions = append(dev.Partitions, p)
		}

		dev.NumPartitions = uint32(len(dev.Partitions))
		layout.Devices = append(layout.Devices, dev)
	}

	return layout, nil
}
