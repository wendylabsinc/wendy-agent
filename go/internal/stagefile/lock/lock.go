package lock

import (
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

// Save writes f to path as YAML.
func (f *File) Save(path string) error {
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
