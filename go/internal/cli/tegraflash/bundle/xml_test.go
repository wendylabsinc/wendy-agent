package bundle

import (
	"strings"
	"testing"
)

const rcmbootXML = `<?xml version="1.0"?>
<partition_layout version="01.00.0000">
    <device type="rcm" instance="0" sector_size="512" num_sectors="262144">
        <partition name="mb1" type="mb1_bootloader">
            <filename> mb1_t264_prod.bin </filename>
        </partition>
        <partition name="psc_bl1" type="psc_bl1">
            <filename> psc_bl1_t264_prod.bin </filename>
        </partition>
        <partition name="mb2-applet" type="mb2_applet">
            <filename> applet_t264.bin </filename>
        </partition>
        <partition name="MEM_BCT" type="mem_boot_config_table">
            <filename> </filename>
        </partition>
        <partition name="MEM_DTB" type="mem_dtb">
            <filename></filename>
        </partition>
    </device>
    <device type="spi" instance="0" sector_size="512" num_sectors="131072">
        <partition name="mb1" type="mb1_bootloader">
            <filename> mb1_t264_prod.bin </filename>
        </partition>
    </device>
</partition_layout>`

func TestParseRCMImages(t *testing.T) {
	images, err := ParseRCMImages([]byte(rcmbootXML))
	if err != nil {
		t.Fatalf("ParseRCMImages() error = %v", err)
	}
	want := []RCMImage{
		{Name: "mb1", Type: "mb1_bootloader", Filename: "mb1_t264_prod.bin"},
		{Name: "psc_bl1", Type: "psc_bl1", Filename: "psc_bl1_t264_prod.bin"},
		{Name: "mb2-applet", Type: "mb2_applet", Filename: "applet_t264.bin"},
	}
	if len(images) != len(want) {
		t.Fatalf("len(images) = %d, want %d; got %+v", len(images), len(want), images)
	}
	for i, got := range images {
		if got != want[i] {
			t.Errorf("images[%d] = %+v, want %+v", i, got, want[i])
		}
	}
}

func TestParseRCMImages_NoRCMDevice(t *testing.T) {
	xml := `<?xml version="1.0"?>
<partition_layout version="01.00.0000">
    <device type="spi" instance="0" sector_size="512" num_sectors="131072">
        <partition name="mb1" type="mb1_bootloader">
            <filename> mb1_t264_prod.bin </filename>
        </partition>
    </device>
</partition_layout>`
	_, err := ParseRCMImages([]byte(xml))
	if err == nil {
		t.Fatal("ParseRCMImages() expected error for XML with no rcm device, got nil")
	}
	if !strings.Contains(err.Error(), "no rcm device") {
		t.Errorf("error = %q, want it to mention 'no rcm device'", err.Error())
	}
}

func TestParseRCMImages_AllEmpty(t *testing.T) {
	xml := `<?xml version="1.0"?>
<partition_layout version="01.00.0000">
    <device type="rcm" instance="0" sector_size="512" num_sectors="262144">
        <partition name="MEM_BCT" type="mem_boot_config_table">
            <filename> </filename>
        </partition>
    </device>
</partition_layout>`
	images, err := ParseRCMImages([]byte(xml))
	if err != nil {
		t.Fatalf("ParseRCMImages() unexpected error: %v", err)
	}
	if len(images) != 0 {
		t.Errorf("expected 0 images when all filenames empty, got %d", len(images))
	}
}
