// Package flashpack locates and opens a Jetson AGX Thor (T264) flashpack — the
// single self-describing artifact produced by the builder (scripts/make-thor-
// flashpack.sh) that wendy downloads, extracts and flashes. It resolves cache-first
// so a locally-planted artifact is used even before it is published online.
//
// Layout (see the builder script):
//
//	flashpack/
//	  manifest.json
//	  stage1/                         RCM-boot images (sent verbatim)
//	  stage2/out/flash_workspace/     FileToFlash.txt + signed partition images
//	  stage2/out/tools/               bootburn's sibling tools/ (ToolsVersion.txt …)
//	  stage2/bundle/unified_flash/…/bootburn   NVIDIA bootburn scripts
//	  stage2/pyyaml/                  pure-python PyYAML (+ LICENSE) bootburn imports
//
// Manifest is the authoritative definition of manifest.json: the builder script must
// emit exactly these fields. Fields are marked [consumed] (the flash logic depends
// on them) or [provenance] (recorded for humans inspecting the artifact; not read by
// code — keep them lean).
package flashpack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SupportedSchema is the manifest schema version this wendy understands. A flashpack
// declaring a newer schema is refused (forward-incompatible).
const SupportedSchema = 1

// ErrNotInCache is returned by Resolve when no extracted tree or .tar.zst for the
// requested version is present in the cache. The caller may download the artifact
// to TarballCachePath and retry.
var ErrNotInCache = errors.New("flashpack not in cache")

// clearCacheHint is appended to integrity errors; the cached copy is untrusted and
// the user should discard it and re-fetch.
const clearCacheHint = "the cached flashpack is corrupt — clear it with `wendy cache clear` and re-run"

// FileMeta is one entry in the manifest's integrity map.
type FileMeta struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is the parsed manifest.json (the authoritative schema — see package doc).
type Manifest struct {
	Schema         int    `json:"schema"`          // [consumed] format version; checked against SupportedSchema
	WendyOSVersion string `json:"wendyos_version"` // [consumed] shown to the user
	DefaultMemBCT  string `json:"default_membct"`  // [consumed] stage-1 membct filename (RAMCODE/2)

	// [consumed] stage-1 bootROM image filenames, in the exact order the bootROM
	// expects them. Driving this from data lets a future BSP reorder the chain
	// without a wendy code change. Empty falls back to the built-in default order.
	Stage1SendOrder []string `json:"stage1_send_order"`

	// [consumed] where the major trees live, relative to the flashpack root.
	Layout struct {
		Stage1         string `json:"stage1"`
		FlashWorkspace string `json:"flash_workspace"`
	} `json:"layout"`

	// [consumed] integrity map for stage-1 files (path relative to root → sha256/size).
	// Stage-2 images are NOT listed: bootburn verifies each one device-side after
	// writing (its own MD5 column), and the download itself is checksummed, so the
	// only un-covered surface is the verbatim-sent stage-1 set.
	Files map[string]FileMeta `json:"files"`

	// [provenance] recorded for humans; not read by code.
	Board         string `json:"board"`
	Machine       string `json:"machine"`
	Chip          string `json:"chip"`
	Ramcode       string `json:"ramcode"`
	ChipSKU       string `json:"chip_sku"`
	PyYAMLVersion string `json:"pyyaml_version"`
}

// Flashpack is an opened, extracted flashpack on disk.
type Flashpack struct {
	Root     string
	Manifest Manifest
}

// Stage1Dir holds the RCM-boot images the bringup stage sends verbatim.
func (f *Flashpack) Stage1Dir() string {
	return filepath.Join(f.Root, f.Manifest.Layout.Stage1)
}

// FlashWorkspaceDir is the bootburn -P workspace (contains flash-images/).
func (f *Flashpack) FlashWorkspaceDir() string {
	return filepath.Join(f.Root, f.Manifest.Layout.FlashWorkspace)
}

// WorkspaceOutDir is the generated "out" dir (flash_workspace/ + sibling tools/);
// bootburn reads flash_workspace/../tools, so the sibling layout must be preserved.
func (f *Flashpack) WorkspaceOutDir() string {
	return filepath.Dir(f.FlashWorkspaceDir())
}

// BundleDir holds NVIDIA's bootburn scripts. The path is fixed by the flashpack
// layout the builder produces (stage2/bundle), so it is not a manifest field.
func (f *Flashpack) BundleDir() string {
	return filepath.Join(f.Root, "stage2", "bundle")
}

// PyYAMLDir holds the pure-python PyYAML package (a yaml/ dir + LICENSE) that
// bootburn imports; the host adds it to PYTHONPATH at flash time. Fixed by the
// flashpack layout (stage2/pyyaml), so it is not a manifest field.
func (f *Flashpack) PyYAMLDir() string {
	return filepath.Join(f.Root, "stage2", "pyyaml")
}

// MemBCT is the membct filename to send in stage 1 (selected by on-board RAMCODE/2
// on the builder; all variants are shipped under stage1/).
func (f *Flashpack) MemBCT() string { return f.Manifest.DefaultMemBCT }

// FlashpackName is the cache basename for a given version (without extension).
func FlashpackName(version string) string {
	return "jetson-agx-thor-" + version + ".flashpack"
}

// TarballCachePath is where a downloaded/planted flashpack tarball for version lives.
func TarballCachePath(cacheDir, version string) string {
	return filepath.Join(cacheDir, FlashpackName(version)+".tar.zst")
}

// Resolve returns the flashpack for version, cache-first: an already-extracted tree,
// else a planted/downloaded .tar.zst extracted on demand (atomically). The stage-1
// files are integrity-checked against the manifest before the flashpack is returned,
// so a corrupt cache aborts the flash rather than producing an opaque bootROM
// failure. Returns ErrNotInCache when nothing is present for the caller to download.
func Resolve(cacheDir, version string) (*Flashpack, error) {
	if version == "" {
		return nil, fmt.Errorf("a version is required to resolve a Thor flashpack")
	}
	extracted := filepath.Join(cacheDir, FlashpackName(version))
	if fp, err := open(extracted); err == nil {
		return fp, fp.verifyStage1()
	}
	tarball := TarballCachePath(cacheDir, version)
	if _, err := os.Stat(tarball); err == nil {
		if err := extractZstTar(tarball, extracted); err != nil {
			return nil, fmt.Errorf("extracting %s: %w", filepath.Base(tarball), err)
		}
		fp, err := open(extracted)
		if err != nil {
			return nil, err
		}
		return fp, fp.verifyStage1()
	}
	return nil, fmt.Errorf("%w: version %s in %s", ErrNotInCache, version, cacheDir)
}

func open(root string) (*Flashpack, error) {
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest.json: %w", err)
	}
	if m.Schema > SupportedSchema {
		return nil, fmt.Errorf("flashpack schema %d is newer than this wendy supports (%d); update wendy", m.Schema, SupportedSchema)
	}
	if m.Layout.Stage1 == "" || m.Layout.FlashWorkspace == "" {
		return nil, fmt.Errorf("flashpack manifest missing layout")
	}
	return &Flashpack{Root: root, Manifest: m}, nil
}

// verifyStage1 checks every stage-1 file in the manifest against its recorded size
// and SHA-256. A missing or mismatched file aborts (the cached copy is untrusted).
func (f *Flashpack) verifyStage1() error {
	for rel, meta := range f.Manifest.Files {
		if !strings.HasPrefix(rel, "stage1/") {
			continue
		}
		p := filepath.Join(f.Root, rel)
		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("flashpack missing %s: %w; %s", rel, err, clearCacheHint)
		}
		if info.Size() != meta.Size {
			return fmt.Errorf("flashpack %s wrong size (got %d, want %d); %s", rel, info.Size(), meta.Size, clearCacheHint)
		}
		sum, err := sha256File(p)
		if err != nil {
			return fmt.Errorf("hashing %s: %w", rel, err)
		}
		if !strings.EqualFold(sum, meta.SHA256) {
			return fmt.Errorf("flashpack %s checksum mismatch (got %s, want %s); %s", rel, sum, meta.SHA256, clearCacheHint)
		}
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
