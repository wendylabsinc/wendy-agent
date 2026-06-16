package commands

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

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

func TestDockerConfigAuthNoFile(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	if _, ok := dockerConfigAuth("ghcr.io"); ok {
		t.Error("expected no creds when config.json is absent")
	}
}
