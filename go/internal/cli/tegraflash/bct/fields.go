// Package bct assembles the NVIDIA Tegra T264 (Thor, chip 0x26) Boot ROM BCT
// (BR BCT) binary from compiled device-tree (DTB) inputs. It is a Go port of
// the relevant parts of NVIDIA's tegrabct_v2 tool.
//
// The BR BCT is an 8192-byte (0x2000) structure consumed by the Boot ROM. The
// signed section [0x1600, 0x2000) is hashed with SHA-512 (the 64-byte digest
// landing at offset 0x5d8) and optionally signed. This package builds the body
// up to (but not including) the hash/signature, which are produced later.
package bct

// Field describes one entry in the tegrabct_v2 s_BrBctFields descriptor table.
// Each entry encodes how a logical BR BCT field maps onto the binary: how many
// elements it has (Count), where the region starts (Offset), and its total
// byte size (Size). For array fields (Count > 1) the per-element stride is
// Size/Count.
type Field struct {
	Count  uint32
	Offset uint32
	Size   uint32
}

// brBctFields is the T264 s_BrBctFields table, transcribed verbatim from
// docs/tegraflash-re/tegrabct_v2-br-bct-format.md section 2. The table lives
// at 0x08179238 in the reference binary and has 28 entries of 12 bytes each
// ({uint32 count, uint32 offset, uint32 size}). Indices 2, 3, 5, 6, 12 and 18
// are all-zero / unused for T264.
var brBctFields = [28]Field{
	0:  {Count: 1, Offset: 0x1c10, Size: 0x10},  // 16-byte field near end
	1:  {Count: 1, Offset: 0x0618, Size: 0xb10}, // signature / key-modulus region
	2:  {},                                      // unused
	3:  {},                                      // unused
	4:  {Count: 1, Offset: 0x1600, Size: 0xa00}, // signed section
	5:  {},                                      // unused
	6:  {},                                      // unused
	7:  {Count: 12, Offset: 0x1648, Size: 0xc0}, // per-slot params (12 x 0x10)
	8:  {Count: 12, Offset: 0x1650, Size: 0x4},  // array sub-field
	9:  {Count: 12, Offset: 0x1648, Size: 0x4},  // array sub-field
	10: {Count: 12, Offset: 0x164c, Size: 0x4},  // array sub-field
	11: {Count: 1, Offset: 0x1200, Size: 0x400}, // customer data part 1
	12: {},                                      // unused
	13: {Count: 1, Offset: 0x1c48, Size: 0x4},   // SecureDebugControlNoneEcid
	14: {Count: 1, Offset: 0x1c4c, Size: 0x4},   // SecureDebugControlEcid
	15: {Count: 1, Offset: 0x1800, Size: 0x400}, // customer data part 2 / SDRAM
	16: {Count: 1, Offset: 0x1e34, Size: 0x20},  // 32-byte hash/key
	17: {Count: 1, Offset: 0x1e54, Size: 0x20},  // 32-byte hash/key
	18: {},                                      // unused
	19: {Count: 1, Offset: 0x1c54, Size: 0x4},   // preprod_dev_sign
	20: {Count: 1, Offset: 0x1c5c, Size: 0x4},   // bf_bl_allbits (RMW bitfield)
	21: {Count: 1, Offset: 0x05d4, Size: 0x4},   // word just before the hash
	22: {Count: 1, Offset: 0x1e20, Size: 0x4},   // u32_anti_clone_select
	23: {Count: 1, Offset: 0x05d8, Size: 0x40},  // crypto hash (SHA-512, 64 B)
	24: {Count: 1, Offset: 0x1c50, Size: 0x4},   // SecureDebugControlToggleAllowed
	25: {Count: 1, Offset: 0x1c58, Size: 0x4},   // tpm_measurement_algorithm
	26: {Count: 1, Offset: 0x176c, Size: 0x2},   // u16_fuse_revoke_bitmap
	27: {Count: 1, Offset: 0x17ff, Size: 0x1},   // u8_l4t_marker_based_selection
}

// mb1BctFields is the T264 s_Mb1BctFields table, transcribed verbatim from
// docs/tegraflash-re/mb1-mem-mb2-bct-formats.md ("s_Mb1BctFields", 90 entries).
// The table lives at 0x0817943c in the reference binary, 0x438 bytes = 90
// entries of 12 bytes each ({uint32 count, uint32 offset, uint32 size}). Only
// the populated entries are listed in the RE doc; every other index is a
// reserved/unused all-zero slot. The highest field (idx 89 at 0x9568 + 4 =
// 0x956c) fits within the MB1 BCT buffer.
//
// Several entries describe array fields (Count > 1); for those the per-element
// stride is Size/Count. Notable clusters: idx 0-4 the SDRAM parameter index
// records (27 sets), idx 13-14 the large config blocks, idx 15-16 the embedded
// MISC sub-block header, idx 40-80 scattered MISC scalar/array fields, and idx
// 86-89 the trailing fields near the end of the buffer.
var mb1BctFields = [90]Field{
	0:  {Count: 27, Offset: 0x5e0, Size: 1728}, // SDRAM parameter set table (27 x 64-byte index records)
	1:  {Count: 27, Offset: 0x5e0, Size: 4},    // per-set field
	2:  {Count: 27, Offset: 0x5e4, Size: 4},    // per-set field
	3:  {Count: 27, Offset: 0x5e8, Size: 4},    // per-set field
	4:  {Count: 27, Offset: 0x5ec, Size: 4},    // per-set field
	5:  {Count: 1, Offset: 0x0c, Size: 4},
	6:  {Count: 1, Offset: 0x450, Size: 8},
	7:  {Count: 1, Offset: 0x444, Size: 1}, // byte flags (idx 7-11 contiguous)
	8:  {Count: 1, Offset: 0x445, Size: 1},
	9:  {Count: 1, Offset: 0x446, Size: 1},
	10: {Count: 1, Offset: 0x447, Size: 1},
	11: {Count: 1, Offset: 0x448, Size: 1},
	13: {Count: 9, Offset: 0xca0, Size: 36},    // 9 x 4-byte array
	14: {Count: 84, Offset: 0xce0, Size: 2352}, // 84 x 28-byte array (large config block)
	15: {Count: 1, Offset: 0x1610, Size: 4},    // start of embedded MISC block
	16: {Count: 1, Offset: 0x1618, Size: 8},
	26: {Count: 1, Offset: 0xcc8, Size: 8},
	40: {Count: 1, Offset: 0x2b8, Size: 8},
	41: {Count: 1, Offset: 0x2d8, Size: 4}, // contiguous 4-byte fields (idx 41-50)
	42: {Count: 1, Offset: 0x2dc, Size: 4},
	43: {Count: 1, Offset: 0x2e0, Size: 4},
	44: {Count: 1, Offset: 0x2e4, Size: 4},
	45: {Count: 1, Offset: 0x2e8, Size: 4},
	46: {Count: 1, Offset: 0x2ec, Size: 4},
	47: {Count: 1, Offset: 0x2f0, Size: 4},
	48: {Count: 1, Offset: 0x2f4, Size: 4},
	49: {Count: 1, Offset: 0x2f8, Size: 4},
	50: {Count: 1, Offset: 0x2fc, Size: 4},
	52: {Count: 1, Offset: 0xcd0, Size: 8},
	53: {Count: 1, Offset: 0x1638, Size: 8},
	54: {Count: 1, Offset: 0x19b0, Size: 4},
	55: {Count: 1, Offset: 0x2c0, Size: 8},
	56: {Count: 1, Offset: 0x2c8, Size: 8},
	57: {Count: 1, Offset: 0x438, Size: 4}, // idx 57-59 contiguous 4-byte fields
	58: {Count: 1, Offset: 0x43c, Size: 4},
	59: {Count: 1, Offset: 0x440, Size: 4},
	62: {Count: 1, Offset: 0x2528, Size: 4},
	63: {Count: 1, Offset: 0x252c, Size: 4},
	65: {Count: 1, Offset: 0x2530, Size: 4}, // idx 65-71 contiguous 4-byte fields
	66: {Count: 1, Offset: 0x2534, Size: 4},
	67: {Count: 1, Offset: 0x2538, Size: 4},
	68: {Count: 1, Offset: 0x253c, Size: 4},
	69: {Count: 1, Offset: 0x2540, Size: 4},
	70: {Count: 1, Offset: 0x2544, Size: 4},
	71: {Count: 1, Offset: 0x2548, Size: 4},
	74: {Count: 1, Offset: 0x1640, Size: 4}, // idx 74-77 contiguous 4-byte fields
	75: {Count: 1, Offset: 0x1644, Size: 4},
	76: {Count: 1, Offset: 0x1648, Size: 4},
	77: {Count: 1, Offset: 0x164c, Size: 4},
	78: {Count: 1, Offset: 0xcd8, Size: 8},
	79: {Count: 1, Offset: 0x1650, Size: 8},
	80: {Count: 1, Offset: 0x1664, Size: 4},
	81: {Count: 2, Offset: 0x3e8, Size: 48}, // 2 x 24-byte array
	82: {Count: 1, Offset: 0x3a0, Size: 72},
	85: {Count: 1, Offset: 0x10, Size: 8},
	86: {Count: 1, Offset: 0x92a8, Size: 4},
	87: {Count: 1, Offset: 0x92e0, Size: 8},
	88: {Count: 1, Offset: 0x92e8, Size: 8},
	89: {Count: 1, Offset: 0x9568, Size: 4}, // near end of buffer
}
