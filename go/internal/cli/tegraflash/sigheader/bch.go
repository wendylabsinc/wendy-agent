// Package sigheader implements the 8192-byte NVDA Boot Component Header (BCH)
// that tegrahost_v2 --appendsigheader prepends to T264 boot images.
//
// The only supported mode is zerosbk: SHA-512 integrity digests are computed and
// stored, but no RSA/ECC key material or signature is written.
package sigheader

import (
	"encoding/binary"
	"fmt"
)

const (
	// bchSize is the fixed size of the Boot Component Header in bytes (0x2000).
	bchSize = 0x2000

	// magic is the 4-byte ASCII identifier at offset 0x0000.
	magic = "NVDA"

	// headerVersion is the value written at offset 0x1AA0.
	headerVersion = 1

	// offHeaderVersion is the BCH offset of the header_version field.
	offHeaderVersion = 0x1AA0

	// offComponentCount is the BCH offset of the component count (0x0FE0).
	offComponentCount = 0x0FE0

	// offCompTable is the base offset of the component table (0x1400).
	// Each entry is 0xA0 (160) bytes wide.
	offCompTable = 0x1400

	// offStage1 is the BCH offset of the stage1_components[0] descriptor (0x1EE0).
	offStage1 = 0x1EE0
)

// Component table field offsets relative to the start of each entry.
const (
	compOffTypeID      = 0x00
	compOffImgLen      = 0x04
	compOffLoadAddr    = 0x08
	compOffEntryPt     = 0x0C
	compOffAlignedLen  = 0x20
	compOffSHA512      = 0x60
)

// stage1_components[0] field offsets relative to offStage1.
const (
	stage1OffTypeID   = 0x00
	stage1OffImgLen   = 0x04
	stage1OffLoadAddr = 0x08
	stage1OffEntryPt  = 0x0C
)

// AppendSigHeader prepends an 8192-byte BCH (Boot Component Header) to payload
// and returns the combined []byte in zerosbk mode.
//
// magicID is the 4-character binary-type identifier written into the descriptor
// fields (e.g. [4]byte{'M','B','1','B'}).
//
// imageType is a human-readable type name used to look up the load and entry
// addresses via LoadEntryFor. Pass an empty string to get the default addresses.
//
// The returned slice is bchSize + len(payload) bytes. All three SHA-512 digests
// are computed and stored; the RSA/ECC key and signature regions remain zero.
func AppendSigHeader(payload []byte, magicID [4]byte, imageType string) ([]byte, error) {
	if len(payload) < 64 {
		return nil, fmt.Errorf("sigheader: payload too short (%d bytes); minimum 64", len(payload))
	}

	buf := make([]byte, bchSize+len(payload))

	// Magic "NVDA" at offset 0x0000.
	copy(buf[0:4], magic)

	// header_version = 1 at 0x1AA0.
	binary.LittleEndian.PutUint32(buf[offHeaderVersion:], headerVersion)

	// Component count = 1 at 0x0FE0.
	binary.LittleEndian.PutUint32(buf[offComponentCount:], 1)

	imgLen := uint32(len(payload))
	load, entry := LoadEntryFor(imageType)

	// Component table entry 0 at 0x1400.
	comp := buf[offCompTable:]
	copy(comp[compOffTypeID:compOffTypeID+4], magicID[:])
	binary.LittleEndian.PutUint32(comp[compOffImgLen:], imgLen)
	binary.LittleEndian.PutUint32(comp[compOffLoadAddr:], load)
	binary.LittleEndian.PutUint32(comp[compOffEntryPt:], entry)
	binary.LittleEndian.PutUint32(comp[compOffAlignedLen:], imgLen)

	// stage1_components[0] descriptor at 0x1EE0.
	s1 := buf[offStage1:]
	copy(s1[stage1OffTypeID:stage1OffTypeID+4], magicID[:])
	binary.LittleEndian.PutUint32(s1[stage1OffImgLen:], imgLen)
	binary.LittleEndian.PutUint32(s1[stage1OffLoadAddr:], load)
	binary.LittleEndian.PutUint32(s1[stage1OffEntryPt:], entry)

	// Payload at 0x2000.
	copy(buf[bchSize:], payload)

	// Compute and store the three SHA-512 digests in the required order.
	recomputeDigests(buf)

	return buf, nil
}

// LoadEntryFor returns the load address and entry point for a given image type
// name, as hard-coded in NvTegraT264FillMb1NvHeader.
//
// Recognised type names:
//   - "mb1_bootloader" and "psc_bl1" -> 0x40040000 / 0x40040000
//   - "tboot"                        -> 0x00110000 / 0x00120400
//   - "" (no type name)              -> 0x00000000 / 0x00000000
//
// All other names fall back to the unnamed default: 0x50000000 / 0x50000000.
//
// The empty-string case reproduces tegrahost_v2 behaviour when --appendsigheader
// is invoked with --magicid but no separate image-type argument: the type-name
// strncmp path is skipped and load/entry remain zero (as confirmed by the golden
// fixture at offsets 0x1EE8/0x1EEC).
func LoadEntryFor(imageType string) (load, entry uint32) {
	switch imageType {
	case "":
		return 0x00000000, 0x00000000
	case "mb1_bootloader", "psc_bl1":
		return 0x40040000, 0x40040000
	case "tboot":
		return 0x00110000, 0x00120400
	default:
		return 0x50000000, 0x50000000
	}
}
