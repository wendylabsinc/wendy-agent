package catalog

import "testing"

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
		cfg := e.DefaultConfig
		if err := cfg.Validate(); err != nil {
			t.Errorf("entry %q default config is invalid: %v", e.Name, err)
		}
		if seen[e.Name] {
			t.Errorf("duplicate catalog entry name %q", e.Name)
		}
		seen[e.Name] = true
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
