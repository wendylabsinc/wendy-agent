package lock

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// File is the on-disk lockfile: a record of every floating reference a
// Stagefile source resolves to, plus a hash of the source it was resolved
// against.
type File struct {
	Version    int               `yaml:"version"`
	SourceHash string            `yaml:"sourceHash"`
	Images     map[string]string `yaml:"images"`
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
	return os.WriteFile(path, data, 0o644)
}
