package commands

import "testing"

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
