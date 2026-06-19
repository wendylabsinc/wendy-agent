package bct

import (
	"encoding/binary"
	"fmt"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/dtb"
)

// brBctSize is the fixed size of the BR BCT body in bytes (0x2000 = 8192).
const brBctSize = 0x2000

// bctbMagic is the SDRAM / MEM BCT ASCII magic written into the signed section
// at offset 0x1610 by NvTegraT264BctInitStaticFields. Stored little-endian as
// the word 0x42544342, i.e. the bytes "BCTB".
const bctbMagicOffset = 0x1610

// Version-byte destination offsets, written directly relative to the BR BCT
// base by NvTegraT264DtbParseVersion (see dtb-field-mapping section 2.1).
const (
	offVerMajor   = 0x1708
	offVerMinor   = 0x1709
	offRatchetLvl = 0x170a
)

// BRBCTInputs carries the three compiled DTB blobs that feed the BR BCT.
// DevParamDTB supplies the /brbct/ node, SDRAMDTB the /sdram/ node, and
// WB0SDRAMDTB the warm-boot /sdram/ node (the second SDRAM set).
type BRBCTInputs struct {
	DevParamDTB []byte
	SDRAMDTB    []byte
	WB0SDRAMDTB []byte
}

// genericBrBctField mirrors one entry of the 8-entry table at 0x08160c64 used
// by NvTegraT264DtbParseGenericBrBctField: a leaf property name, the BR BCT
// field index it targets, and the value width in bytes.
type genericBrBctField struct {
	prop     string
	fieldIdx int
	size     uint32
}

// genericBrBctFields is the 8-entry generic-field table (dtb-field-mapping
// section 2.1). Each property is parsed as an integer and written into the
// named BR BCT field via setData.
var genericBrBctFields = []genericBrBctField{
	{"SecureDebugControlEcid", 14, 4},
	{"SecureDebugControlNoneEcid", 13, 4},
	{"SecureDebugControlToggleAllowed", 24, 4},
	{"preprod_dev_sign", 19, 4},
	{"u32_anti_clone_select", 22, 4},
	{"tpm_measurement_algorithm", 25, 4},
	{"u16_fuse_revoke_bitmap", 26, 2},
	{"u8_l4t_marker_based_selection", 27, 1},
}

// bfBlBit mirrors one entry of the 27-entry table at 0x0817e158 used by
// NvTegraT264DtbParseBrBctBfBlBits: a property name and the bit position/width
// it occupies within BR BCT field index 20 (bf_bl_allbits, 4 bytes).
type bfBlBit struct {
	prop  string
	shift uint
	width uint
}

// bfBlBits is the 27-entry bf_bl_allbits table (dtb-field-mapping section 2.1),
// all width 1 with ascending shift 0..26.
var bfBlBits = []bfBlBit{
	{"bf_bl_gpio_select_boot_chain_1b", 0, 1},
	{"bf_bl_mb1_debug_production_1b", 1, 1},
	{"bf_bl_sc7_rf_debug_production_1b", 2, 1},
	{"bf_bl_psc_bl_debug_production_1b", 3, 1},
	{"bf_bl_psc_rf_debug_production_1b", 4, 1},
	{"bf_bl_psc_fw_debug_production_1b", 5, 1},
	{"bf_bl_bpmp_debug_production_1b", 6, 1},
	{"bf_bl_bpmp_ist_debug_production_1b", 7, 1},
	{"bf_bl_mce_debug_production_1b", 8, 1},
	{"bf_bl_ist_ccplex_debug_production_1b", 9, 1},
	{"bf_bl_ist_fw_debug_production_1b", 10, 1},
	{"bf_bl_rtc_rail_violation_detect_1b", 11, 1},
	{"bf_bl_cust_nv_ccplex_dfd_en_1b", 12, 1},
	{"bf_bl_debug_with_test_keys_1b", 13, 1},
	{"bf_bl_debug_with_test_keys_during_psc_debug_1b", 14, 1},
	{"bf_bl_disable_bootrom_clock_boost_1b", 15, 1},
	{"bf_bl_disable_pscrom_clk_boost_1b", 16, 1},
	{"bf_bl_enable_scpm_reset", 17, 1},
	{"bf_bl_skip_oem_auth_diag_boot", 18, 1},
	{"bf_bl_diag_boot", 19, 1},
	{"bf_bl_bpmp_diag_boot", 20, 1},
	{"bf_bl_l0_ist", 21, 1},
	{"bf_bl_l1_ist", 22, 1},
	{"bf_bl_dft_access_allowed", 23, 1},
	{"bf_bl_tpm_present_1b", 24, 1},
	{"bf_bl_igbfw_debug_production_1b", 25, 1},
	{"bf_bl_tsec_debug_production_1b", 26, 1},
}

// bfBlUnsignedBits is the 2-entry bf_bl_unsigned_allbits table at 0x0817e29c,
// targeting BR BCT field index 21 (offset 0x05d4, 4 bytes).
var bfBlUnsignedBits = []bfBlBit{
	{"bf_bl_unordered_key_protection_mask", 0, 15},
	{"bf_bl_socket_1", 15, 1},
}

// BuildBRBCT assembles the 8192-byte BR BCT body from the supplied DTB inputs.
// It writes the static magic, parses the dev_param /brbct/ node into its
// tractable fields, and packs as much of the SDRAM region as is cleanly
// understood. The crypto hash (0x5d8) and signature (0x618) are left zero;
// those are produced in the signing step (Task 9).
//
// The SDRAM packing (NvTegraT264PackSdramParams) is only partially understood;
// see brbct_test.go (TestBRBCTSdramGap) for the precise unfinished region.
func BuildBRBCT(in BRBCTInputs) ([]byte, error) {
	out := make([]byte, brBctSize)

	// Static magic: "BCTB" at 0x1610 (NvTegraT264BctInitStaticFields).
	copy(out[bctbMagicOffset:bctbMagicOffset+4], []byte("BCTB"))

	if len(in.DevParamDTB) > 0 {
		if err := parseDevParam(out, in.DevParamDTB); err != nil {
			return nil, fmt.Errorf("bct: dev_param: %w", err)
		}
	}

	if err := packSDRAM(out, in); err != nil {
		return nil, fmt.Errorf("bct: sdram: %w", err)
	}

	return out, nil
}

// setData writes a little-endian value of the given size into the BR BCT field
// region named by fieldIdx, mirroring NvBctT264BrBctSetData. value is masked to
// the field width. Only the 1-, 2- and 4-byte widths used by the dev_param
// parsers are supported.
func setData(out []byte, fieldIdx int, value uint32, size uint32) error {
	if fieldIdx < 0 || fieldIdx >= len(brBctFields) {
		return fmt.Errorf("field index %d out of range", fieldIdx)
	}
	f := brBctFields[fieldIdx]
	off := int(f.Offset)
	if off+int(size) > len(out) {
		return fmt.Errorf("field %d write [0x%x,0x%x) out of bounds", fieldIdx, off, off+int(size))
	}
	switch size {
	case 1:
		out[off] = byte(value)
	case 2:
		binary.LittleEndian.PutUint16(out[off:], uint16(value))
	case 4:
		binary.LittleEndian.PutUint32(out[off:], value)
	default:
		return fmt.Errorf("unsupported setData size %d", size)
	}
	return nil
}

// getData reads a little-endian uint32 from a BR BCT field region. Returns 0 if
// the field offset would read past the buffer (a guard against future table
// transcription errors; all current fields are well within the 0x2000 buffer).
func getData(out []byte, fieldIdx int) uint32 {
	f := brBctFields[fieldIdx]
	if int(f.Offset)+4 > len(out) {
		return 0
	}
	return binary.LittleEndian.Uint32(out[f.Offset:])
}

// packSDRAM reproduces the SDRAM portion of the BR BCT. In the reference tool
// the /sdram/ node (~3210 named properties per set) is parsed into a 0x3228-
// byte-per-set struct and then relocated/packed by NvTegraT264PackSdramParams
// into the BR BCT. That pack is only partially decoded (see the RE doc section
// 5.3 and TestBRBCTSdramGap): the populated output in the golden BR BCT is a
// small per-slot summary in the field-7 array region [0x1648, 0x1708) totalling
// 8 non-zero bytes across 4 of the 12 slots. The transform that derives those
// summary words from the parsed SDRAM sets is not yet reproduced, so this
// function intentionally writes nothing rather than emit incorrect bytes.
//
// This is the documented, deliberate gap for this increment. It is exercised
// and characterized by TestBRBCTSdramGap so a reviewer can see exactly what is
// missing.
func packSDRAM(out []byte, in BRBCTInputs) error {
	// Validate the SDRAM inputs parse, so a malformed blob is reported here
	// rather than silently ignored, but do not emit any bytes yet.
	for _, blob := range [][]byte{in.SDRAMDTB, in.WB0SDRAMDTB} {
		if len(blob) == 0 {
			continue
		}
		if _, err := dtb.ParseFDT(blob); err != nil {
			return fmt.Errorf("parse sdram fdt: %w", err)
		}
	}
	return nil
}

// parseDevParam parses the /brbct/ node of the dev_param DTB into the BR BCT,
// reproducing BrBctT264PropertyCfgHandler and its sub-handlers for the
// tractable (named-field and bit-field) properties.
func parseDevParam(out []byte, blob []byte) error {
	fdt, err := dtb.ParseFDT(blob)
	if err != nil {
		return fmt.Errorf("parse fdt: %w", err)
	}
	if !fdt.HasNode("/brbct") {
		return nil // nothing to do
	}

	// Generic named fields (8-entry table): each is a single integer cell.
	for _, g := range genericBrBctFields {
		v, ok := fdt.PropertyU32("/brbct", g.prop)
		if !ok {
			continue
		}
		if err := setData(out, g.fieldIdx, v, g.size); err != nil {
			return err
		}
	}

	// Version bytes written directly at fixed base-relative offsets.
	for _, vp := range []struct {
		prop string
		off  int
	}{
		{"u8_ver_major", offVerMajor},
		{"u8_ver_minor", offVerMinor},
		{"u8_ratchet_level", offRatchetLvl},
	} {
		if v, ok := fdt.PropertyU32("/brbct", vp.prop); ok {
			out[vp.off] = byte(v)
		}
	}

	// bf_bl_allbits: read-modify-write bit inserts into field index 20.
	if err := parseBitfield(out, fdt, "/brbct/bf_bl_allbits", 20, bfBlBits); err != nil {
		return err
	}
	// bf_bl_unsigned_allbits: RMW into field index 21.
	if err := parseBitfield(out, fdt, "/brbct/bf_bl_unsigned_allbits", 21, bfBlUnsignedBits); err != nil {
		return err
	}

	return nil
}

// parseBitfield reproduces NvTegraT264DtbParseBrBctBfBlBits: for each named
// child property present in the node, it clears the target bit range in the
// destination field and ORs in the masked value.
func parseBitfield(out []byte, fdt *dtb.FDT, node string, fieldIdx int, bits []bfBlBit) error {
	if !fdt.HasNode(node) {
		return nil
	}
	acc := getData(out, fieldIdx)
	for _, b := range bits {
		v, ok := fdt.PropertyU32(node, b.prop)
		if !ok {
			continue
		}
		mask := uint32((1 << b.width) - 1)
		acc &^= mask << b.shift
		acc |= (v & mask) << b.shift
	}
	return setData(out, fieldIdx, acc, 4)
}
