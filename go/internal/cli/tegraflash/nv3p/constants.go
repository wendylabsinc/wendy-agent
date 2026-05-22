// Protocol constants translated from NVIDIA tegrarcm nv3p.h
// (BSD 3-Clause License, Copyright (c) 2011 NVIDIA CORPORATION)
package nv3p

// Protocol versions.
// T234 applet (nv3p v1) uses Version; T264 bootROM and MB1 use VersionT264.
const (
	Version     = uint32(1)
	VersionT264 = uint32(3)
)

// Commands
const (
	CmdGetPlatformInfo = uint32(0x01)
	CmdGetBCT          = uint32(0x02)
	CmdDLBCT           = uint32(0x04)
	CmdDLBL            = uint32(0x06)
	CmdDLPartition     = uint32(0x08)
	CmdStatus          = uint32(0x0a)
	CmdReset           = uint32(0x0e)

	// CmdDownloadT264 is the tegrarcm_v2 "download file" command used by
	// T264 MB1 for both bct_mem and blob transfers. It reuses the 0x02 slot
	// but sends a 56-byte args struct that includes the type name string.
	CmdDownloadT264 = uint32(0x02)
)

// NACK codes
const (
	NACKSuccess = uint32(0x1)
	NACKBadCmd  = uint32(0x2)
	NACKBadData = uint32(0x3)
)

// Packet types for nv3p v1 (T234 applet).
const (
	PacketTypeCmd       = uint32(0x1)
	PacketTypeData      = uint32(0x2)
	PacketTypeEncrypted = uint32(0x3)
	PacketTypeACK       = uint32(0x4)
	PacketTypeNACK      = uint32(0x5)
)

// Packet types for nv3p v3 (T264 bootROM and MB1).
// The enum shifted: ACK=3 and NACK=4 in v3.
// Device-initiated packet types (device → host):
//   type=8  STATUS_CMD   carries status/error info after a transfer
//   type=9  RESPONSE_CMD carries the data_size of the pending DATA transfer
const (
	PacketTypeACKv3      = uint32(0x3)
	PacketTypeNACKv3     = uint32(0x4)
	PacketTypeStatusCmd  = uint32(0x8)
	PacketTypeResponseCmd = uint32(0x9)
)

// Packet header/footer sizes in bytes (from nv3p.h NV3P_PACKET_SIZE_* constants)
const (
	sizeBasic     = 3 * 4 // v1: version + type + sequence (12 bytes)
	sizeBasicV3   = 4 * 4 // v3: version + type + sequence + reserved (16 bytes)
	sizeCommand   = 2 * 4 // args_length + command
	sizeData      = 1 * 4 // data_length
	sizeEncrypted = 1 * 4
	sizeFooter    = 1 * 4 // checksum
	sizeACK       = 0 * 4
	sizeNACK      = 1 * 4 // nack_code

	StringMax = 32
)

// Boot device types (NV3P_DEV_TYPE_*)
const (
	DevTypeNAND          = uint32(0x1)
	DevTypeEMMC          = uint32(0x2)
	DevTypeSPI           = uint32(0x3)
	DevTypeIDE           = uint32(0x4)
	DevTypeNANDx16       = uint32(0x5)
	DevTypeSNOR          = uint32(0x6)
	DevTypeMuxOneNAND    = uint32(0x7)
	DevTypeMobileLBANAND = uint32(0x8)
)
