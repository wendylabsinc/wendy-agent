// Package partition implements the tegraparser_v2 --pt partition-table
// serializer: it parses NVIDIA's partition-layout XML and produces the
// binary .bin format consumed by tegrabct_v2 and tegraflash tooling.
package partition

// Layout is the parsed top-level partition layout.
type Layout struct {
	Version uint32
	Devices []Device
}

// Device represents one <device> element.
type Device struct {
	Type          uint32
	Instance      uint32
	NumPartitions uint32
	SectorSize    uint32
	NumSectors    uint64
	Erase         bool
	Partitions    []Partition
}

// Partition represents one <partition> element.
type Partition struct {
	Name                 string
	ID                   uint32
	TypeID               uint32
	FSType               uint32
	Size                 uint64
	StartLocation        uint64
	FileSystemAttribute  uint64
	AlignBoundary        uint64
	AllocationAttribute  uint32
	PercentReserved      uint32
	OEMSign              bool
	AuthGroup            bool
	TypeGUID             [16]byte
	UniqueGUID           [16]byte
	Filename             string
}

// partitionTypeIDs maps XML type= strings to numeric partition-type IDs.
// Transcribed verbatim from s_PartitionType in tegraparser_v2 .data.
// Section 4.1 of tegraflash-re/tegraparser_v2.md.
var partitionTypeIDs = map[string]uint32{
	"boot_config_table":                   0x01,
	"bootloader":                          0x02,
	"nv_data":                             0x03,
	"data":                                0x04,
	"master_boot_record":                  0x05,
	"extended_boot_record":                0x06,
	"primary_gpt":                         0x07,
	"secondary_gpt":                       0x08,
	"bootloader_stage2":                   0x09,
	"fuse_bypass":                         0x0b,
	"config_table":                        0x0c,
	"wb0":                                 0x0d,
	"WB0":                                 0x0d,
	"sc7_resume_fw":                       0x0d,
	"mts_preboot":                         0x0e,
	"mts_bootpack":                        0x0f,
	"mts_mce":                             0x10,
	"mts_proper":                          0x11,
	"br_boot_config_table":                0x12,
	"mb1_boot_config_table":               0x13,
	"mb1_bootloader":                      0x14,
	"spe_fw":                              0x15,
	"early_spe_fw":                        0x15,
	"dram_ecc":                            0x16,
	"black_list_info":                     0x17,
	"extended_can_fw":                     0x18,
	"mb2_bootloader":                      0x19,
	"protective_master_boot_record":       0x1a,
	"smd":                                 0x1b,
	"rollback_prevention_bypass":          0x1c,
	"xusb_fw":                             0x1d,
	"ist_ucode":                           0x1e,
	"bpmp_ist":                            0x1f,
	"ist_config":                          0x20,
	"fskp_bin":                            0x21,
	"extended_spe_fw":                     0x22,
	"sce_fw":                              0x23,
	"ape_fw":                              0x24,
	"rce_fw":                              0x25,
	"bpmp_fw":                             0x26,
	"mem_boot_config_table":               0x27,
	"bl_dtb":                              0x28,
	"tos":                                 0x29,
	"eks":                                 0x2a,
	"bpmp_fw_dtb":                         0x2b,
	"mb2_applet":                          0x2c,
	"kernel":                              0x2d,
	"kernel_dtb":                          0x2e,
	"psc_fw":                              0x2f,
	"psc_bl1":                             0x30,
	"dce_fw":                              0x31,
	"tsec_fw":                             0x32,
	"ccplex_ist":                          0x33,
	"nvdec":                               0x34,
	"mb2rf":                               0x35,
	"psc_rf":                              0x36,
	"oitv":                                0x37,
	"fsi_fw":                              0x38,
	"bootloader_dtb":                      0x39,
	"nvlink-fw":                           0x3a,
	"uphy_ucode":                          0x3b,
	"atf":                                 0x3c,
	"hafnium":                             0x3d,
	"secure_partition":                    0x3e,
	"brbct_section_unsigned":              0x3f,
	"brbct_section_signed":                0x40,
	"mce-coverage":                        0x41,
	"uefi_vars":                           0x42,
	"uefi_ftw":                            0x43,
	"ras_error_logs":                      0x44,
	"early_boot_vars":                     0x45,
	"cmet":                                0x46,
	"pva_fw":                              0x47,
	"meta_data":                           0x48,
	"oem":                                 0x49,
	"erst":                                0x4a,
	"hpse_pkg":                            0x4b,
	"sb_pkg":                              0x4c,
	"ape1_fw":                             0x4d,
	"aon_fw":                              0x4e,
	"igpu-boot-fw":                        0x4f,
	"secure_hv":                           0x50,
	"rist_tid":                            0x51,
	"mem_dtb":                             0x52,
	"hpse_bl":                             0x53,
	"sb_bl":                               0x54,
	"hpse_om":                             0x55,
	"sb_om":                               0x56,
	"plat-misc-cfg":                       0x8d,
	"backup_secondary_gpt":                0x8e,
	"diag_cpu_fw":                         0x8f,
	"diag_bpmp_fw":                        0x90,
	"rce1_fw":                             0x91,
	"ist-testimg":                         0x92,
	"ist-rti":                             0x93,
	"dce_fw_dtb":                          0x95,
}

// deviceTypeIDs maps XML device type= strings to numeric device-type IDs.
// Transcribed verbatim from s_DeviceType in tegraparser_v2 .data.
// Section 4.3 of tegraflash-re/tegraparser_v2.md.
var deviceTypeIDs = map[string]uint32{
	"sdmmc_boot": 0,
	"sdmmc_user": 1,
	"snor":       2,
	"spi":        3,
	"sata":       4,
	"sdcard":     6,
	"ufs":        7,
	"ufs_user":   8,
	"external":   9,
	"rcm":        10,
	"nvme":       12,
}

// filesystemTypeIDs maps filesystem type strings to numeric IDs.
// Transcribed verbatim from s_FilesystemType in tegraparser_v2 .data.
// Section 4.4 of tegraflash-re/tegraparser_v2.md.
var filesystemTypeIDs = map[string]uint32{
	"basic":    1,
	"enhanced": 2,
	"ext2":     3,
	"yaffs2":   4,
	"ext3":     5,
	"ext4":     6,
	"qnx":      7,
}

// PartitionTypeID returns the numeric partition-type ID for the given type
// string and true, or (0, false) if the string is not in the table.
func PartitionTypeID(name string) (uint32, bool) {
	id, ok := partitionTypeIDs[name]
	return id, ok
}

// DeviceTypeID returns the numeric device-type ID for the given type string
// and true, or (0, false) if the string is not in the table.
func DeviceTypeID(name string) (uint32, bool) {
	id, ok := deviceTypeIDs[name]
	return id, ok
}

// FilesystemTypeID returns the numeric filesystem-type ID for the given type
// string and true, or (0, false) if the string is not in the table.
func FilesystemTypeID(name string) (uint32, bool) {
	id, ok := filesystemTypeIDs[name]
	return id, ok
}
