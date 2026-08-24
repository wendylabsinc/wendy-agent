package lock

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/gpu"
)

// File is the on-disk lockfile: a record of every floating reference a
// Stagefile source resolves to, plus a hash of the source it was resolved
// against.
type File struct {
	Version    int               `yaml:"version"`
	SourceHash string            `yaml:"sourceHash"`
	Images     map[string]string `yaml:"images"`
	// ManagedBases records which revision of Wendy's managed base catalog
	// selected each image ref. The digest remains in Images; this metadata is
	// the refresh trigger that lets a newer Wendy release deliberately update a
	// maintained channel without making it float on every build.
	ManagedBases map[string]ManagedBase `yaml:"managedBases,omitempty"`
	// Downloads pins each download URL to the sha256 of the bytes it served
	// when it was first resolved. Omitted from the file entirely when a
	// Stagefile declares no downloads, so existing lockfiles don't grow an
	// empty key on their next build.
	Downloads map[string]string `yaml:"downloads,omitempty"`
	// CUDA pins, per GPU architecture, the profile a `cuda:` stage was built
	// against — CUDA version, wheel index and runtime package set.
	//
	// This is the one input to a GPU build that lives in the CLI rather than
	// in the project, so without a pin, upgrading the CLI could rebuild an
	// app against a different CUDA runtime with nothing in the project
	// changing. Recording it puts that on the same footing as a base image
	// digest: visible in the diff, and changed only deliberately.
	//
	// Keyed by gpu_arch ("sm_87"), so one project that deploys to several
	// boards accumulates one entry per board rather than fighting over one.
	CUDA map[string]gpu.Profile `yaml:"cuda,omitempty"`
}

// ManagedBase is the lockfile state for one declarative base: channel.
type ManagedBase struct {
	Ref      string `yaml:"ref"`
	Revision int    `yaml:"revision"`
}

// ResolveCUDA returns the profile to build arch against: the one already
// pinned in the lockfile if there is one, otherwise the compiler's current
// profile for arch, recorded into f so later builds reuse it.
func (f *File) ResolveCUDA(arch string) (gpu.Profile, error) {
	if pinned, ok := f.CUDA[arch]; ok {
		pinned.Arch = arch
		return pinned, nil
	}
	p, err := gpu.ProfileFor(arch)
	if err != nil {
		return gpu.Profile{}, err
	}
	if f.CUDA == nil {
		f.CUDA = map[string]gpu.Profile{}
	}
	f.CUDA[arch] = p
	return p, nil
}

// Load reads and parses the lockfile at path. A missing file is not an
// error: it returns (nil, nil), since "no lockfile yet" is a normal state
// before the first `stagefile lock` run.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse lockfile %s: %w", path, err)
	}
	return &f, nil
}

// Save writes f to path as YAML. An unchanged lockfile is left untouched
// (no write, no mtime churn): the compiler calls Save on every build, and a
// rewrite of identical bytes would still fire file watchers — `wendy watch`
// would cancel and restart its own deploy on every cycle.
func (f *File) Save(path string) error {
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	// Temp file + rename so a concurrent Load (parallel service builds
	// sharing a context dir) never observes a truncated lockfile.
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
