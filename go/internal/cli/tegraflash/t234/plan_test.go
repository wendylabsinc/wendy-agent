package t234

import "testing"

// agxOrinEMMCPlan is the real jetson-agx-orin-devkit-emmc layout (17
// partitions, from nvflashxmlparse -t rootfs of the 0.16.1 bundle), in flash
// layout order.
var agxOrinEMMCPlan = []Partition{
	{Number: 3, Name: "A_kernel", SizeSectors: 262144, File: "boot.img"},
	{Number: 4, Name: "A_kernel-dtb", SizeSectors: 1536, File: "kernel_tegra234-p3737-0000+p3701-0005-nv.dtb"},
	{Number: 5, Name: "A_reserved_on_user", SizeSectors: 64768},
	{Number: 6, Name: "B_kernel", SizeSectors: 262144, File: "boot.img"},
	{Number: 7, Name: "B_kernel-dtb", SizeSectors: 1536, File: "kernel_tegra234-p3737-0000+p3701-0005-nv.dtb"},
	{Number: 8, Name: "B_reserved_on_user", SizeSectors: 64768},
	{Number: 9, Name: "recovery", SizeSectors: 163840},
	{Number: 10, Name: "recovery-dtb", SizeSectors: 1024},
	{Number: 11, Name: "esp", SizeSectors: 131072, File: "esp.img", TypeGuid: "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"},
	{Number: 12, Name: "recovery_alt", SizeSectors: 163840},
	{Number: 13, Name: "recovery-dtb_alt", SizeSectors: 1024},
	{Number: 14, Name: "esp_alt", SizeSectors: 131072},
	{Number: 15, Name: "UDA", SizeSectors: 819200},
	{Number: 1, Name: "APP", SizeSectors: 29360128, File: "wendyos-image.ext4"},
	{Number: 2, Name: "APP_b", SizeSectors: 29360128, File: "wendyos-image.ext4"},
	{Number: 16, Name: "config", SizeSectors: 524288, File: "config-partition.fat32.img", TypeGuid: "EBD0A0A2-B9E5-4433-87C0-68B6B72699C7"},
	{Number: 17, Name: "data", SizeSectors: 1048576, TypeGuid: "0FC63DAF-8483-4772-8E79-3D69D8477DE4"},
}

// sgdisk's placement of the same layout on the 64 GB eMMC (from a real
// `sgdisk --new` run, keyed by partition number). place() must reproduce it.
var sgdiskStarts = map[int]int64{
	1: 2074624, 2: 31434752, 3: 2048, 4: 264192, 5: 266240, 6: 331776,
	7: 593920, 8: 595968, 9: 661504, 10: 825344, 11: 827392, 12: 958464,
	13: 1122304, 14: 1124352, 15: 1255424, 16: 60794880, 17: 61319168,
}

func testPlan() *Plan {
	parts := make([]Partition, len(agxOrinEMMCPlan))
	copy(parts, agxOrinEMMCPlan)
	return &Plan{RootfsDevice: "mmcblk0", Partitions: parts}
}

func TestPlacementMatchesSgdisk(t *testing.T) {
	p := testPlan()
	if err := p.place(); err != nil {
		t.Fatal(err)
	}
	for _, part := range p.Partitions {
		want, ok := sgdiskStarts[part.Number]
		if !ok {
			t.Fatalf("no sgdisk reference for partition %d", part.Number)
		}
		if part.StartSector != want {
			t.Errorf("partition %d (%s): start %d, sgdisk placed it at %d", part.Number, part.Name, part.StartSector, want)
		}
	}
}
