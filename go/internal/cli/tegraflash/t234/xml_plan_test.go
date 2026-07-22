package t234

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeXMLFixture(t *testing.T, allocation string) (layout, images string) {
	t.Helper()
	images = t.TempDir()
	if err := os.WriteFile(filepath.Join(images, "rootfs.img"), []byte("rootfs"), 0o644); err != nil {
		t.Fatal(err)
	}
	xml := `<?xml version="1.0"?><partition_layout version="01.00.0000">
<device type="external" instance="0" sector_size="512">
<partition name="master_boot_record" type="protective_master_boot_record"><allocation_policy>sequential</allocation_policy><filesystem_type>basic</filesystem_type><size>512</size><allocation_attribute>8</allocation_attribute></partition>
<partition name="APP" id="1" type="data"><allocation_policy>sequential</allocation_policy><filesystem_type>basic</filesystem_type><size>1048576</size><allocation_attribute>8</allocation_attribute><filename>rootfs.img</filename><partition_type_guid>0FC63DAF-8483-4772-8E79-3D69D8477DE4</partition_type_guid></partition>
<partition name="data" id="2" type="data"><allocation_policy>sequential</allocation_policy><filesystem_type>ext4</filesystem_type><size>1048576</size><allocation_attribute>` + allocation + `</allocation_attribute></partition>
<partition name="secondary_gpt" type="secondary_gpt"><allocation_policy>sequential</allocation_policy><filesystem_type>basic</filesystem_type><size>0xFFFFFFFFFFFFFFFF</size><allocation_attribute>8</allocation_attribute></partition>
</device></partition_layout>`
	layout = filepath.Join(images, "initrd-flash.xml")
	if err := os.WriteFile(layout, []byte(xml), 0o644); err != nil {
		t.Fatal(err)
	}
	return layout, images
}

func TestLoadXMLPlanAndFillToEnd(t *testing.T) {
	layout, images := writeXMLFixture(t, "0x808")
	p, err := LoadXMLPlan(layout, images, "nvme0n1")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := p.ResolveForDevice(10000)
	if err != nil {
		t.Fatal(err)
	}
	data := resolved.Partitions[1]
	if !data.FillToEnd || data.StartSector+data.SizeSectors != 10000-gptBackupSectors {
		t.Fatalf("fill partition = %+v", data)
	}
}

func TestLoadXMLPlanRejectsMissingImage(t *testing.T) {
	layout, images := writeXMLFixture(t, "8")
	if err := os.Remove(filepath.Join(images, "rootfs.img")); err != nil {
		t.Fatal(err)
	}
	_, err := LoadXMLPlan(layout, images, "nvme0n1")
	if err == nil || !strings.Contains(err.Error(), "references rootfs.img") {
		t.Fatalf("missing-image error = %v", err)
	}
}

func TestLoadXMLPlanRejectsUnsafeMetadataBeforeWriting(t *testing.T) {
	layout, images := writeXMLFixture(t, "8")
	data, err := os.ReadFile(layout)
	if err != nil {
		t.Fatal(err)
	}
	badGUID := strings.Replace(string(data), "</partition_type_guid>", "-not-a-guid</partition_type_guid>", 1)
	if err := os.WriteFile(layout, []byte(badGUID), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadXMLPlan(layout, images, "nvme0n1"); err == nil || !strings.Contains(err.Error(), "partition_type_guid") {
		t.Fatalf("invalid-GUID error = %v", err)
	}

	if _, err := LoadXMLPlan(layout, images, "sda"); err == nil || !strings.Contains(err.Error(), "unsupported rootfs device") {
		t.Fatalf("unsupported-rootfs error = %v", err)
	}
}

func TestLoadXMLPlanRejectsTrailingRootElement(t *testing.T) {
	layout, images := writeXMLFixture(t, "8")
	f, err := os.OpenFile(layout, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("<partition_layout/>"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadXMLPlan(layout, images, "nvme0n1"); err == nil {
		t.Fatal("trailing root element was accepted")
	}
}

func TestLoadXMLPlanMatchesAGXOrinSgdiskGolden(t *testing.T) {
	dir := t.TempDir()
	var xml strings.Builder
	xml.WriteString(`<?xml version="1.0"?><partition_layout version="01.00.0000"><device type="sdmmc_user" instance="3" sector_size="512">`)
	for _, part := range agxOrinEMMCPlan {
		fmt.Fprintf(&xml, `<partition name="%s" id="%d" type="data"><allocation_policy>sequential</allocation_policy><filesystem_type>basic</filesystem_type><size>%d</size><allocation_attribute>8</allocation_attribute>`, part.Name, part.Number, part.SizeSectors*sectorSize)
		if part.File != "" {
			if err := os.WriteFile(filepath.Join(dir, part.File), []byte(part.Name), 0o644); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&xml, `<filename>%s</filename>`, part.File)
		}
		if part.TypeGuid != "" {
			fmt.Fprintf(&xml, `<partition_type_guid>%s</partition_type_guid>`, part.TypeGuid)
		}
		xml.WriteString(`</partition>`)
	}
	xml.WriteString(`</device></partition_layout>`)
	layout := filepath.Join(dir, "initrd-flash.xml")
	if err := os.WriteFile(layout, []byte(xml.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := LoadXMLPlan(layout, dir, "mmcblk0")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range plan.Partitions {
		if want := sgdiskStarts[part.Number]; part.StartSector != want {
			t.Errorf("partition %d (%s) starts at %d, want golden %d", part.Number, part.Name, part.StartSector, want)
		}
	}
}
