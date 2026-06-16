// Package catalog provides a curated list of common container apps that can be
// installed onto a device with `wendy device apps install`.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

//go:embed catalog.json
var catalogJSON []byte

// Entry is a single installable app in the curated catalog.
type Entry struct {
	Name        string `json:"name"`
	Image       string `json:"image"`
	Description string `json:"description"`
	// Category groups related apps in the install picker (e.g. "Database").
	Category      string              `json:"category"`
	DefaultConfig appconfig.AppConfig `json:"defaultConfig"`
}

// Load parses the embedded catalog.
func Load() ([]Entry, error) {
	var entries []Entry
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		return nil, fmt.Errorf("parsing embedded catalog: %w", err)
	}
	return entries, nil
}

// Lookup returns the catalog entry with the given name.
func Lookup(name string) (Entry, bool) {
	entries, err := Load()
	if err != nil {
		return Entry{}, false
	}
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}
