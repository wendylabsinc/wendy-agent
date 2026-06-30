package optimize

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestDiscoverComposeServiceRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	// A malicious context that escapes the project tree must not be analyzed.
	cfg := &appconfig.AppConfig{
		Services: map[string]*appconfig.ServiceConfig{
			"evil": {Context: "../../../../../../etc"},
		},
	}
	targets, err := DiscoverTargets(dir, cfg, "arm64")
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("traversal context must be skipped, got %d targets: %+v", len(targets), targets)
	}
}

func TestIsWithinDir(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		target string
		want   bool
	}{
		{base, true},
		{filepath.Join(base, "svc"), true},
		{filepath.Join(base, "a", "b"), true},
		{filepath.Join(base, ".."), false},
		{filepath.Join(base, "..", "sibling"), false},
		{filepath.Join(base, "..", "..", "etc"), false},
	}
	for _, c := range cases {
		if got := isWithinDir(base, c.target); got != c.want {
			t.Errorf("isWithinDir(%q, %q) = %v, want %v", base, c.target, got, c.want)
		}
	}
}

func TestResolveArch(t *testing.T) {
	if got := resolveArch(""); got != "arm64" {
		t.Fatalf("resolveArch(\"\") = %q, want arm64", got)
	}
	if got := resolveArch("amd64"); got != "amd64" {
		t.Fatalf("resolveArch(\"amd64\") = %q, want amd64", got)
	}
}

func TestDiscoverSingleDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", "FROM python:3.12-slim\n")
	writeFile(t, dir, "requirements.txt", "torch==2.3.0+cpu\n")

	targets, err := DiscoverTargets(dir, nil, "arm64")
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	tg := targets[0]
	if tg.Kind != KindDockerfile {
		t.Fatalf("kind = %v, want KindDockerfile", tg.Kind)
	}
	if tg.Dockerfile == nil || len(tg.Dockerfile.Instructions) == 0 {
		t.Fatalf("Dockerfile not parsed")
	}
	if tg.Requirements == nil || len(tg.Requirements.Packages) != 1 {
		t.Fatalf("requirements not attached")
	}
	if tg.Arch != "arm64" {
		t.Fatalf("arch = %q, want arm64", tg.Arch)
	}
}

func TestDiscoverNativeSwift(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Package.swift", "// swift-tools-version:6.0\n")
	targets, err := DiscoverTargets(dir, nil, "arm64")
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Kind != KindNativeSwift {
		t.Fatalf("targets = %+v, want one KindNativeSwift", targets)
	}
}

func TestDiscoverNothing(t *testing.T) {
	dir := t.TempDir()
	targets, err := DiscoverTargets(dir, nil, "arm64")
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("got %d targets, want 0", len(targets))
	}
}

func TestDiscoverComposeServices(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "api")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, svcDir, "Dockerfile", "FROM golang:1.22\nRUN go build\n")
	cfg := &appconfig.AppConfig{
		Services: map[string]*appconfig.ServiceConfig{
			"api": {Context: "api"},
		},
	}
	targets, err := DiscoverTargets(dir, cfg, "arm64")
	if err != nil {
		t.Fatalf("DiscoverTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	tg := targets[0]
	if tg.Kind != KindComposeService {
		t.Fatalf("kind = %v, want KindComposeService", tg.Kind)
	}
	if tg.Name != "api" {
		t.Fatalf("name = %q, want api", tg.Name)
	}
	if tg.Dir != svcDir {
		t.Fatalf("dir = %q, want %q", tg.Dir, svcDir)
	}
	if tg.Dockerfile == nil || len(tg.Dockerfile.Instructions) == 0 {
		t.Fatalf("Dockerfile not parsed for compose service")
	}
}
