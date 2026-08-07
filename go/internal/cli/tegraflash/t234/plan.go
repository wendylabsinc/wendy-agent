package t234

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// sectorSize is the logical block size of each supported exported rootfs LUN.
// Schema-v2 layout parsing rejects any other value.
const sectorSize = 512

// alignSectors is the partition start alignment, matching sgdisk's default
// (1 MiB) that the bundle's make-sdcard relies on.
const alignSectors = 2048

// Partition is one rootfs-device partition from initrd-flash.xml, in layout
// order (the order determines on-disk placement, not the partition number).
type Partition struct {
	Number      int    `json:"number"`
	Name        string `json:"name"`
	SizeSectors int64  `json:"sizeSectors"`
	// File is the partition's content image, relative to the bundle root;
	// empty for partitions that are created but not written.
	File       string `json:"file"`
	TypeGuid   string `json:"typeGuid"`
	UniqueGuid string `json:"uniqueGuid,omitempty"`
	FsType     string `json:"fsType"`
	FillToEnd  bool   `json:"fillToEnd,omitempty"`

	// StartSector is computed by placement, not read from the XML.
	StartSector int64 `json:"-"`
}

// Plan is the validated rootfs-device portion of initrd-flash.xml.
type Plan struct {
	RootfsDevice string      `json:"rootfsDevice"`
	Partitions   []Partition `json:"partitions"`
}

type flashXML struct {
	XMLName xml.Name    `xml:"partition_layout"`
	Version string      `xml:"version,attr"`
	Devices []xmlDevice `xml:"device"`
}

type xmlDevice struct {
	Type       string         `xml:"type,attr"`
	Instance   string         `xml:"instance,attr"`
	SectorSize string         `xml:"sector_size,attr"`
	Partitions []xmlPartition `xml:"partition"`
}

type xmlPartition struct {
	Name                string `xml:"name,attr"`
	ID                  string `xml:"id,attr"`
	Type                string `xml:"type,attr"`
	AllocationPolicy    string `xml:"allocation_policy"`
	FilesystemType      string `xml:"filesystem_type"`
	Size                string `xml:"size"`
	Filename            string `xml:"filename"`
	AllocationAttribute string `xml:"allocation_attribute"`
	TypeGUID            string `xml:"partition_type_guid"`
	UniqueGUID          string `xml:"unique_guid"`
}

// LoadXMLPlan strictly parses the signed initrd-flash layout shipped in a
// schema-v2 flashpack. It selects only the rootfs device, assigns NVIDIA's
// partition numbers, and validates every referenced image before USB is
// touched. No nvflashxmlparse or other host binary is invoked.
func LoadXMLPlan(layoutPath, imagesDir, rootfsDevice string) (*Plan, error) {
	if rootfsDevice != "nvme0n1" && rootfsDevice != "mmcblk0" {
		return nil, fmt.Errorf("unsupported rootfs device %q", rootfsDevice)
	}
	f, err := os.Open(layoutPath)
	if err != nil {
		return nil, fmt.Errorf("opening partition layout: %w", err)
	}
	defer f.Close()
	dec := xml.NewDecoder(f)
	dec.Strict = true
	var doc flashXML
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing partition layout XML: %w", err)
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing trailing partition layout XML: %w", err)
		}
		if data, ok := tok.(xml.CharData); ok && strings.TrimSpace(string(data)) == "" {
			continue
		}
		switch tok.(type) {
		case xml.Comment, xml.Directive, xml.ProcInst:
			continue
		default:
			return nil, fmt.Errorf("partition layout contains content after the root element")
		}
	}
	if doc.XMLName.Local != "partition_layout" || len(doc.Devices) == 0 {
		return nil, fmt.Errorf("partition layout has no devices")
	}
	wantType := "external"
	if rootfsDevice == "mmcblk0" {
		wantType = "sdmmc_user"
	}
	var candidates []xmlDevice
	for _, d := range doc.Devices {
		if strings.TrimSpace(d.Type) == wantType {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("partition layout has %d %s rootfs devices (want exactly one)", len(candidates), wantType)
	}
	d := candidates[0]
	ss, err := parseXMLUint(d.SectorSize)
	if err != nil || ss != sectorSize {
		return nil, fmt.Errorf("rootfs device sector_size %q is unsupported (want %d)", d.SectorSize, sectorSize)
	}
	p := &Plan{RootfsDevice: rootfsDevice}
	used := map[int]bool{}
	next := 1
	for _, xp := range d.Partitions {
		typ := strings.TrimSpace(xp.Type)
		if typ == "protective_master_boot_record" || typ == "primary_gpt" || typ == "secondary_gpt" {
			continue
		}
		if strings.TrimSpace(xp.AllocationPolicy) != "sequential" {
			return nil, fmt.Errorf("partition %s has unsupported allocation policy %q", xp.Name, xp.AllocationPolicy)
		}
		sizeBytes, err := parseXMLUint(xp.Size)
		if err != nil || sizeBytes == 0 || sizeBytes%sectorSize != 0 {
			return nil, fmt.Errorf("partition %s has invalid sector-aligned size %q", xp.Name, xp.Size)
		}
		number := 0
		if strings.TrimSpace(xp.ID) != "" {
			n64, err := parseXMLUint(xp.ID)
			if err != nil || n64 < 1 || n64 > gptNumEntries {
				return nil, fmt.Errorf("partition %s has invalid id %q", xp.Name, xp.ID)
			}
			number = int(n64)
		} else {
			for used[next] {
				next++
			}
			number = next
		}
		if number < 1 || number > gptNumEntries {
			return nil, fmt.Errorf("partition %s number %d is out of range", xp.Name, number)
		}
		if used[number] {
			return nil, fmt.Errorf("duplicate partition id %d", number)
		}
		used[number] = true
		if number >= next {
			next = number + 1
		}
		attrs, err := parseXMLUint(xp.AllocationAttribute)
		if err != nil {
			return nil, fmt.Errorf("partition %s has invalid allocation_attribute %q", xp.Name, xp.AllocationAttribute)
		}
		file := strings.TrimSpace(xp.Filename)
		if file != "" {
			clean := filepath.Clean(filepath.FromSlash(file))
			if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
				return nil, fmt.Errorf("partition %s has unsafe filename %q", xp.Name, file)
			}
			file = clean
			st, err := os.Stat(filepath.Join(imagesDir, file))
			if err != nil {
				return nil, fmt.Errorf("partition %s references %s: %w", xp.Name, file, err)
			}
			if !st.Mode().IsRegular() || st.Size() > int64(sizeBytes) {
				return nil, fmt.Errorf("partition %s image %s (%d bytes) does not fit (%d bytes)", xp.Name, file, st.Size(), sizeBytes)
			}
		}
		fsType := strings.ToLower(strings.TrimSpace(xp.FilesystemType))
		switch fsType {
		case "", "basic", "ext4", "fat32", "vfat":
		default:
			return nil, fmt.Errorf("partition %s requests unsupported blank filesystem %q", xp.Name, fsType)
		}
		typeGUID := strings.TrimSpace(xp.TypeGUID)
		if typeGUID != "" {
			probe := make([]byte, 16)
			if err := putGUID(probe, typeGUID); err != nil {
				return nil, fmt.Errorf("partition %s has invalid partition_type_guid %q", xp.Name, typeGUID)
			}
		}
		uniqueGUID := strings.TrimSpace(xp.UniqueGUID)
		if uniqueGUID != "" {
			probe := make([]byte, 16)
			if err := putGUID(probe, uniqueGUID); err != nil {
				// NVIDIA templates may retain symbolic APPUUID/APPUUID_b tokens;
				// make-sdcard generates fresh GUIDs for those at flash time.
				if upper := strings.ToUpper(uniqueGUID); upper == "APPUUID" || upper == "APPUUID_B" {
					uniqueGUID = ""
				} else {
					return nil, fmt.Errorf("partition %s has invalid unique_guid %q", xp.Name, uniqueGUID)
				}
			}
		}
		part := Partition{Number: number, Name: strings.TrimSpace(xp.Name), SizeSectors: int64(sizeBytes / sectorSize), File: file,
			TypeGuid: typeGUID, UniqueGuid: uniqueGUID, FsType: fsType, FillToEnd: attrs&0x800 != 0}
		if part.Name == "" {
			return nil, fmt.Errorf("partition id %d has no name", number)
		}
		if len(utf16.Encode([]rune(part.Name))) > 36 {
			return nil, fmt.Errorf("partition name %q is longer than 36 UTF-16 units", part.Name)
		}
		p.Partitions = append(p.Partitions, part)
	}
	if len(p.Partitions) == 0 {
		return nil, fmt.Errorf("rootfs partition layout is empty")
	}
	if err := p.place(); err != nil {
		return nil, err
	}
	return p, nil
}

func parseXMLUint(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty integer")
	}
	return strconv.ParseUint(s, 0, 64)
}

// place assigns start sectors in flash-layout order: each partition starts at
// the next alignSectors-aligned sector after the previous one, the first at
// alignSectors. This reproduces what sgdisk does for the bundle's make-sdcard
// (sequential --new with default alignment); gpt_test.go pins it against a
// real sgdisk run of the AGX Orin eMMC layout.
func (p *Plan) place() error {
	next := int64(alignSectors)
	for i := range p.Partitions {
		part := &p.Partitions[i]
		if part.SizeSectors <= 0 {
			return fmt.Errorf("partition %s has invalid size %d", part.Name, part.SizeSectors)
		}
		start := (next + alignSectors - 1) / alignSectors * alignSectors
		part.StartSector = start
		next = start + part.SizeSectors
		if part.FillToEnd && i != len(p.Partitions)-1 {
			return fmt.Errorf("fill-to-end partition %s must be last", part.Name)
		}
	}
	return nil
}

// resolveForDevice returns a placed copy whose fill-to-end partition consumes
// all usable sectors before the backup GPT, while retaining its XML size as a
// minimum-capacity constraint.
func (p *Plan) ResolveForDevice(deviceSectors int64) (*Plan, error) {
	copyPlan := *p
	copyPlan.Partitions = append([]Partition(nil), p.Partitions...)
	if err := copyPlan.place(); err != nil {
		return nil, err
	}
	lastUsable := deviceSectors - gptBackupSectors - 1
	for i := range copyPlan.Partitions {
		part := &copyPlan.Partitions[i]
		if !part.FillToEnd {
			continue
		}
		available := lastUsable - part.StartSector + 1
		if available < part.SizeSectors {
			return nil, fmt.Errorf("device leaves %d sectors for fill-to-end partition %s, minimum is %d", available, part.Name, part.SizeSectors)
		}
		part.SizeSectors = available
	}
	if copyPlan.MinDeviceSectors() > deviceSectors {
		return nil, fmt.Errorf("device too small: %d sectors, plan needs at least %d", deviceSectors, copyPlan.MinDeviceSectors())
	}
	return &copyPlan, nil
}

// MinDeviceSectors is the smallest device (in sectors) the placed plan fits
// on, including the 33-sector backup GPT.
func (p *Plan) MinDeviceSectors() int64 {
	var end int64
	for _, part := range p.Partitions {
		if e := part.StartSector + part.SizeSectors; e > end {
			end = e
		}
	}
	return end + gptBackupSectors
}
