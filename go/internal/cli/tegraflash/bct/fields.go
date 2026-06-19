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
