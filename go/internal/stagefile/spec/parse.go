package spec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Parse decodes and validates Stagefile source. A non-nil File is only ever
// returned once it has passed Validate. Unknown keys are an error: a
// misspelled or misnested key (`entrypont:`, `user:` under `build:`) would
// otherwise be dropped without a trace — the exact silent drift the format
// exists to prevent.
func Parse(data []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// ParseFile reads and parses the Stagefile at path.
func ParseFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// SourceHash returns the "sha256:<hex>" digest of raw source bytes, used by
// the lockfile to detect a source edit that hasn't been re-locked.
func SourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
