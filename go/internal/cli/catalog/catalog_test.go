package catalog

import (
	"strings"
	"testing"
)

func TestLoadParsesAndValidates(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("catalog is empty")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Name == "" || e.Image == "" {
			t.Errorf("entry %+v missing name or image", e)
		}
		if e.DefaultConfig.AppID == "" {
			t.Errorf("entry %q has empty defaultConfig.appId", e.Name)
		}
		if e.Category == "" {
			t.Errorf("entry %q has empty category", e.Name)
		}
		cfg := e.DefaultConfig
		if err := cfg.Validate(); err != nil {
			t.Errorf("entry %q default config is invalid: %v", e.Name, err)
		}
		// Web-UI entries declare a postStart openURL hook; it must template the
		// device host so the install command can resolve it.
		if cfg.Hooks != nil && cfg.Hooks.PostStart != nil && cfg.Hooks.PostStart.OpenURL != "" {
			if !strings.Contains(cfg.Hooks.PostStart.OpenURL, "WENDY_HOSTNAME") {
				t.Errorf("entry %q openURL %q must reference WENDY_HOSTNAME", e.Name, cfg.Hooks.PostStart.OpenURL)
			}
		}
		if seen[e.Name] {
			t.Errorf("duplicate catalog entry name %q", e.Name)
		}
		seen[e.Name] = true
	}
}

func TestCatalogHasPaperless(t *testing.T) {
	e, ok := Lookup("paperless")
	if !ok {
		t.Fatal("expected paperless in the catalog")
	}
	if e.DefaultConfig.Hooks == nil || e.DefaultConfig.Hooks.PostStart == nil ||
		e.DefaultConfig.Hooks.PostStart.OpenURL == "" {
		t.Error("paperless should declare a web-UI openURL hook")
	}
}

func TestLookup(t *testing.T) {
	e, ok := Lookup("redis")
	if !ok {
		t.Fatal("expected to find redis")
	}
	if e.Image == "" {
		t.Error("redis entry has empty image")
	}
	if _, ok := Lookup("nope-not-real"); ok {
		t.Error("did not expect to find nope-not-real")
	}
}
