package lock

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// File is the on-disk lockfile: a record of every floating reference a
// Stagefile source resolves to, plus a hash of the source it was resolved
// against.
type File struct {
	Version    int               `yaml:"version"`
	SourceHash string            `yaml:"sourceHash"`
	Images     map[string]string `yaml:"images"`
	// Downloads pins each download URL to the sha256 of the bytes it served
	// when it was first resolved. Omitted from the file entirely when a
	// Stagefile declares no downloads, so existing lockfiles don't grow an
	// empty key on their next build.
	Downloads map[string]string `yaml:"downloads,omitempty"`
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
