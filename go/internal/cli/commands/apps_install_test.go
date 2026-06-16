package commands

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/catalog"
)

func TestDeriveAppID(t *testing.T) {
	cases := map[string]string{
		"docker.io/library/redis:7": "redis",
		"redis:7":                   "redis",
		"redis":                     "redis",
		"ghcr.io/home-assistant/home-assistant:stable":   "home-assistant",
		"registry.wendy.sh/org-1/edge-api@sha256:abc123": "edge-api",
		"registry.wendy.sh:5000/a/b:c":                   "b",
	}
	for in, want := range cases {
		if got := deriveAppID(in); got != want {
			t.Errorf("deriveAppID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRegistryHostFromImage(t *testing.T) {
	cases := map[string]string{
		"redis:7":                      "docker.io",
		"library/redis:7":              "docker.io",
		"docker.io/library/redis:7":    "docker.io",
		"ghcr.io/x/y:z":                "ghcr.io",
		"registry.wendy.sh:5000/a/b:c": "registry.wendy.sh:5000",
		"localhost:5000/x:1":           "localhost:5000",
	}
	for in, want := range cases {
		if got := registryHostFromImage(in); got != want {
			t.Errorf("registryHostFromImage(%q)=%q want %q", in, got, want)
		}
	}
}

func TestResolveInstallSourceCatalog(t *testing.T) {
	img, cfg, err := resolveInstallSource("redis")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if img == "" || cfg.AppID == "" {
		t.Errorf("expected catalog image+config, got img=%q appId=%q", img, cfg.AppID)
	}
}

func TestResolveInstallSourceRawImage(t *testing.T) {
	img, cfg, err := resolveInstallSource("ghcr.io/foo/bar:1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if img != "ghcr.io/foo/bar:1" || cfg.AppID != "bar" {
		t.Errorf("got img=%q appId=%q", img, cfg.AppID)
	}
}

func TestResolveInstallSourceEmpty(t *testing.T) {
	if _, _, err := resolveInstallSource(""); err == nil {
		t.Error("expected error for empty arg")
	}
}

func TestResolveRegistryAuthFlags(t *testing.T) {
	auth, err := resolveRegistryAuth("ghcr.io/foo/bar:1", "alice", "tok", false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if auth == nil {
		t.Fatal("expected auth from flags")
	}
	if auth.GetRegistryHost() != "ghcr.io" || auth.GetUsername() != "alice" || auth.GetPassword() != "tok" {
		t.Errorf("got %+v", auth)
	}
}

func TestResolveRegistryAuthAnonymous(t *testing.T) {
	// No flags, and a docker.io image with no docker config -> nil (anonymous).
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	auth, err := resolveRegistryAuth("docker.io/library/redis:7", "", "", false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if auth != nil {
		t.Errorf("expected anonymous (nil) auth, got %+v", auth)
	}
}

func TestDockerConfigAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	enc := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	cfg := `{"auths":{"ghcr.io":{"auth":"` + enc + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := dockerConfigAuth("ghcr.io")
	if !ok {
		t.Fatal("expected creds for ghcr.io")
	}
	if got.GetUsername() != "alice" || got.GetPassword() != "s3cret" {
		t.Errorf("got %q/%q", got.GetUsername(), got.GetPassword())
	}
	if _, ok := dockerConfigAuth("missing.example.com"); ok {
		t.Error("did not expect creds for missing host")
	}
}

func TestDockerConfigAuthExplicitUserPass(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	cfg := `{"auths":{"reg.example.com":{"username":"bob","password":"pw"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := dockerConfigAuth("reg.example.com")
	if !ok {
		t.Fatal("expected creds")
	}
	if got.GetUsername() != "bob" || got.GetPassword() != "pw" {
		t.Errorf("got %q/%q", got.GetUsername(), got.GetPassword())
	}
}

// Docker Hub credentials are stored under the canonical index key, not "docker.io".
func TestDockerConfigAuthDockerHubKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	enc := base64.StdEncoding.EncodeToString([]byte("hubuser:hubpass"))
	cfg := `{"auths":{"https://index.docker.io/v1/":{"auth":"` + enc + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := dockerConfigAuth("docker.io")
	if !ok {
		t.Fatal("expected Docker Hub creds via canonical index key")
	}
	if got.GetUsername() != "hubuser" || got.GetRegistryHost() != "docker.io" {
		t.Errorf("got user=%q host=%q", got.GetUsername(), got.GetRegistryHost())
	}
}

func TestDockerConfigAuthNoFile(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	if _, ok := dockerConfigAuth("ghcr.io"); ok {
		t.Error("expected no creds when config.json is absent")
	}
}

func TestLooksLikeAuthError(t *testing.T) {
	if !looksLikeAuthError(fmt.Errorf("failed: pull access denied, 401 Unauthorized")) {
		t.Error("expected 401 to look like an auth error")
	}
	if looksLikeAuthError(fmt.Errorf("connection refused")) {
		t.Error("did not expect a network error to look like an auth error")
	}
	if looksLikeAuthError(nil) {
		t.Error("nil is not an auth error")
	}
}

func TestCatalogPickerItemsCarryCategory(t *testing.T) {
	entries, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	items := catalogPickerItems(entries)
	if len(items) != len(entries) {
		t.Fatalf("got %d items for %d entries", len(items), len(entries))
	}
	for i, it := range items {
		e := entries[i]
		if it.Type != e.Category {
			t.Errorf("%q: Type=%q want category %q", e.Name, it.Type, e.Category)
		}
		if it.SortKey != e.Category+"_"+e.Name {
			t.Errorf("%q: SortKey=%q want %q", e.Name, it.SortKey, e.Category+"_"+e.Name)
		}
		if v, _ := it.Value.(string); v != e.Name {
			t.Errorf("%q: Value=%v want name", e.Name, it.Value)
		}
	}
}
