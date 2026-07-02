package t234

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PrepDirName is the directory prep.sh creates inside the extracted bundle.
const PrepDirName = "wendy-prep"

// planSchema is the plan.json format version this wendy understands.
const planSchema = 1

// sectorSize is the logical block size of the exported eMMC LUN. The flash
// layout XML declares 512 for every partition (prep rejects anything else
// implicitly: sizes are recorded in 512-byte sectors).
const sectorSize = 512

// alignSectors is the partition start alignment, matching sgdisk's default
// (1 MiB) that the bundle's make-sdcard relies on.
const alignSectors = 2048

// Partition is one rootfs-device partition from plan.json, in flash-layout
// order (the order determines on-disk placement, not the partition number).
type Partition struct {
	Number      int    `json:"number"`
	Name        string `json:"name"`
	SizeSectors int64  `json:"sizeSectors"`
	// File is the partition's content image, relative to the bundle root;
	// empty for partitions that are created but not written.
	File     string `json:"file"`
	TypeGuid string `json:"typeGuid"`
	FsType   string `json:"fsType"`

	// StartSector is computed by Placement, not stored in plan.json.
	StartSector int64 `json:"-"`
}

// Plan is the parsed wendy-prep/plan.json.
type Plan struct {
	Schema       int         `json:"schema"`
	Machine      string      `json:"machine"`
	RootfsDevice string      `json:"rootfsDevice"`
	Partitions   []Partition `json:"partitions"`
}

// LoadPlan reads and validates plan.json from an extracted, prepped bundle,
// and computes each partition's placement.
func LoadPlan(bundleDir string) (*Plan, error) {
	data, err := os.ReadFile(filepath.Join(bundleDir, PrepDirName, "plan.json"))
	if err != nil {
		return nil, fmt.Errorf("reading flash plan: %w", err)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing flash plan: %w", err)
	}
	if p.Schema != planSchema {
		return nil, fmt.Errorf("flash plan schema %d not supported (want %d) — update wendy", p.Schema, planSchema)
	}
	if len(p.Partitions) == 0 {
		return nil, fmt.Errorf("flash plan has no partitions")
	}
	if err := p.place(); err != nil {
		return nil, err
	}
	return &p, nil
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
	}
	return nil
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

// FlashpkgPath returns the prep-built flashpkg.ext4 path.
func FlashpkgPath(bundleDir string) string {
	return filepath.Join(bundleDir, PrepDirName, "flashpkg.ext4")
}
