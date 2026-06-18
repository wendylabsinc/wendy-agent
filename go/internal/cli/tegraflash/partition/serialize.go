package partition

import (
	"bytes"
	"encoding/binary"
)

// Serialize encodes the Layout as the binary partition-table format consumed
// by tegrabct_v2 and related tools.
//
// Binary layout (all fields little-endian):
//
//	12-byte file header
//	num_devices * 32-byte device records
//	For each device:
//	    num_partitions * 128-byte partition records
//	    For each partition:
//	        name\0
//	        filename\0   (always present; empty string if no filename)
//
// Pointer fields (header+0x08, device+0x1c, partition+0x00, partition+0x78)
// are written as zero; callers that need live in-process pointers must
// relocate them via the equivalent of NvTegraPartitionDeSerialize.
func (l *Layout) Serialize() ([]byte, error) {
	var buf bytes.Buffer

	// 12-byte file header.
	writeU32(&buf, l.Version)             // +0x00: packed version
	writeU32(&buf, uint32(len(l.Devices))) // +0x04: num_devices
	writeU32(&buf, 0)                      // +0x08: device-array pointer (zero on disk)

	// Device records (32 bytes each).
	for _, dev := range l.Devices {
		writeU32(&buf, dev.Type)                    // +0x00
		writeU32(&buf, dev.Instance)                // +0x04
		writeU32(&buf, uint32(len(dev.Partitions))) // +0x08
		writeU32(&buf, dev.SectorSize)              // +0x0c
		writeU32(&buf, uint32(dev.NumSectors))      // +0x10 low word
		writeU32(&buf, uint32(dev.NumSectors>>32))  // +0x14 high word
		var erase byte = 1
		if !dev.Erase {
			erase = 0
		}
		buf.WriteByte(erase)     // +0x18
		buf.WriteByte(0)         // +0x19 padding
		buf.WriteByte(0)         // +0x1a padding
		buf.WriteByte(0)         // +0x1b padding
		writeU32(&buf, 0)        // +0x1c partition-array pointer (zero on disk)
	}

	// Per-device: partition records, then strings.
	for _, dev := range l.Devices {
		// Partition records (128 bytes each).
		for _, p := range dev.Partitions {
			var rec [0x80]byte

			// +0x00: name pointer (zero on disk)
			// +0x04: partition_id
			binary.LittleEndian.PutUint32(rec[0x04:], p.ID)
			// +0x08: partition_type id
			binary.LittleEndian.PutUint32(rec[0x08:], p.TypeID)
			// +0x0c: filesystem_type id
			binary.LittleEndian.PutUint32(rec[0x0c:], p.FSType)
			// +0x10..0x17: zeros (two unknown u32 fields)
			// +0x18: start_location (u64, low word first)
			binary.LittleEndian.PutUint32(rec[0x18:], uint32(p.StartLocation))
			binary.LittleEndian.PutUint32(rec[0x1c:], uint32(p.StartLocation>>32))
			// +0x20: size (u64, low word first)
			binary.LittleEndian.PutUint32(rec[0x20:], uint32(p.Size))
			binary.LittleEndian.PutUint32(rec[0x24:], uint32(p.Size>>32))
			// +0x28..0x2f: zeros (two unknown u32 fields)
			// +0x30: allocation_attribute (u32)
			binary.LittleEndian.PutUint32(rec[0x30:], p.AllocationAttribute)
			// +0x34: erase_size (u32)
			binary.LittleEndian.PutUint32(rec[0x34:], p.EraseSize)
			// +0x38: percent_reserved (u32)
			binary.LittleEndian.PutUint32(rec[0x38:], p.PercentReserved)
			// +0x3c: zero (unknown)
			// +0x40: file_system_attribute (u64, low word first)
			binary.LittleEndian.PutUint32(rec[0x40:], uint32(p.FileSystemAttribute))
			binary.LittleEndian.PutUint32(rec[0x44:], uint32(p.FileSystemAttribute>>32))
			// +0x48: oem_sign (u8)
			if p.OEMSign {
				rec[0x48] = 1
			}
			// +0x49: authentication_group (u8)
			if p.AuthGroup {
				rec[0x49] = 1
			}
			// +0x4a..0x4b: padding
			// +0x4c: align_boundary (u64, low word first)
			binary.LittleEndian.PutUint32(rec[0x4c:], uint32(p.AlignBoundary))
			binary.LittleEndian.PutUint32(rec[0x50:], uint32(p.AlignBoundary>>32))
			// +0x54: rollback_level (u8)
			rec[0x54] = p.RollbackLevel
			// +0x55..0x57: padding
			// +0x58: partition_type_guid (16 bytes)
			copy(rec[0x58:], p.TypeGUID[:])
			// +0x68: unique_guid (16 bytes)
			copy(rec[0x68:], p.UniqueGUID[:])
			// +0x78: filename pointer (zero on disk)
			// +0x7c: zero

			buf.Write(rec[:])
		}

		// String region for this device: for each partition, name\0 then filename\0.
		for _, p := range dev.Partitions {
			buf.WriteString(p.Name)
			buf.WriteByte(0)
			buf.WriteString(p.Filename)
			buf.WriteByte(0)
		}
	}

	return buf.Bytes(), nil
}

func writeU32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}
