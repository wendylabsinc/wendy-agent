// Partition layout XML parsing — translated from NVIDIA tegraflash XML format.
// The partition XML is included in the tegraflash tarball (e.g. flash_t234_qspi_sdmmc.xml).
package bundle

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// xmlInt64 is an int64 that also accepts hex literals (e.g. 0xFFFFFFFFFFFFFFFF)
// in XML text content. NVIDIA uses 0xFFFFFFFFFFFFFFFF as a "fill remaining" sentinel.
type xmlInt64 int64

func (x *xmlInt64) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var s string
	if err := d.DecodeElement(&s, &start); err != nil {
		return err
	}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		v, err := strconv.ParseUint(s, 0, 64)
		if err != nil {
			return err
		}
		*x = xmlInt64(int64(v))
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*x = xmlInt64(v)
	return nil
}

// PartitionLayout is the top-level structure of a tegraflash partition XML.
type PartitionLayout struct {
	XMLName xml.Name       `xml:"partition_layout"`
	Devices []DeviceLayout `xml:"device"`
}

// DeviceLayout describes a storage device and its partitions.
type DeviceLayout struct {
	Type       string      `xml:"type,attr"`
	Instance   int         `xml:"instance,attr"`
	SectorSize int         `xml:"sector_size,attr"`
	NumSectors int64       `xml:"num_sectors,attr"`
	Partitions []Partition `xml:"partition"`
}

// IsQSPI reports whether this device is the QSPI NOR flash.
func (d *DeviceLayout) IsQSPI() bool {
	return d.Type == "spi" || d.Type == "qspi"
}

// IsEMMC reports whether this device is the onboard eMMC.
func (d *DeviceLayout) IsEMMC() bool {
	return strings.HasPrefix(d.Type, "sdmmc")
}

// Partition is a single partition entry in the layout XML.
type Partition struct {
	Name     string   `xml:"name,attr"`
	ID       int      `xml:"id,attr"`
	Type     string   `xml:"type,attr"`
	Size     xmlInt64 `xml:"size"`
	Filename string   `xml:"filename"`
}

// HasFile reports whether this partition has a file to write.
func (p *Partition) HasFile() bool {
	return strings.TrimSpace(p.Filename) != ""
}

// IsBCT reports whether this partition is a Boot Configuration Table.
func (p *Partition) IsBCT() bool {
	return p.Type == "boot_config_table" || strings.EqualFold(p.Name, "BCT")
}

// ParseLayout parses a tegraflash partition layout XML.
func ParseLayout(data []byte) (*PartitionLayout, error) {
	var layout PartitionLayout
	if err := xml.Unmarshal(data, &layout); err != nil {
		return nil, err
	}
	return &layout, nil
}

// RCMImage is one entry from the device type="rcm" block of rcmboot-flash.xml.in.
type RCMImage struct {
	Name     string
	Type     string
	Filename string
}

// ParseRCMImages parses a tegraflash partition XML and returns the ordered list
// of partitions in the device type="rcm" block that have non-empty filenames.
// Partitions with empty or whitespace-only filenames (BCT placeholders etc.) are skipped.
func ParseRCMImages(data []byte) ([]RCMImage, error) {
	var layout PartitionLayout
	if err := xml.Unmarshal(data, &layout); err != nil {
		return nil, err
	}
	for _, dev := range layout.Devices {
		if dev.Type != "rcm" {
			continue
		}
		var images []RCMImage
		for _, p := range dev.Partitions {
			if !p.HasFile() {
				continue
			}
			images = append(images, RCMImage{
				Name:     p.Name,
				Type:     p.Type,
				Filename: strings.TrimSpace(p.Filename),
			})
		}
		return images, nil
	}
	return nil, fmt.Errorf("no rcm device block found in partition XML")
}

// RCMImages parses rcmboot-flash.xml.in from the bundle and returns the ordered
// list of RCM-phase images. Partitions without filenames are omitted.
func (b *Bundle) RCMImages() ([]RCMImage, error) {
	data, err := b.ExtractFile("rcmboot-flash.xml.in")
	if err != nil {
		return nil, fmt.Errorf("rcmboot-flash.xml.in not found in bundle: %w", err)
	}
	return ParseRCMImages(data)
}

// preAppletTypes is the ordered sequence of partition types sent via RCM40 before
// the MB2 applet boots. Derived from RE of tegrarcm_v2 mainT23x ImageTable_pre
// (Thor nightly 20260618), entries 2 (mb1), 3 (psc_bl1), 6 (mb2_applet).
// For T264, mb1_bootloader and psc_bl1 live in the "spi" device block;
// mb2_applet lives in the "rcm" device block.
var preAppletTypes = []string{"mb1_bootloader", "psc_bl1", "mb2_applet"}

// ParsePreAppletImages finds the pre-applet RCM images across ALL device blocks,
// returning them in ImageTable_pre order (mb1_bootloader → psc_bl1 → mb2_applet).
// Only partitions with non-empty filenames are included.
func ParsePreAppletImages(data []byte) ([]RCMImage, error) {
	var layout PartitionLayout
	if err := xml.Unmarshal(data, &layout); err != nil {
		return nil, err
	}
	byType := make(map[string]RCMImage)
	for _, dev := range layout.Devices {
		for _, p := range dev.Partitions {
			if !p.HasFile() {
				continue
			}
			if _, seen := byType[p.Type]; !seen {
				byType[p.Type] = RCMImage{
					Name:     p.Name,
					Type:     p.Type,
					Filename: strings.TrimSpace(p.Filename),
				}
			}
		}
	}
	var images []RCMImage
	for _, t := range preAppletTypes {
		if img, ok := byType[t]; ok {
			images = append(images, img)
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("no pre-applet RCM images found (need types: %v)", preAppletTypes)
	}
	return images, nil
}

// PreAppletImages parses rcmboot-flash.xml.in from the bundle and returns only
// the pre-applet images (mb1_bootloader, psc_bl1, mb2_applet) in the order the
// T264 bootROM requires them. These are sent via RCM40 before the applet takes over.
func (b *Bundle) PreAppletImages() ([]RCMImage, error) {
	data, err := b.ExtractFile("rcmboot-flash.xml.in")
	if err != nil {
		return nil, fmt.Errorf("rcmboot-flash.xml.in not found in bundle: %w", err)
	}
	return ParsePreAppletImages(data)
}
