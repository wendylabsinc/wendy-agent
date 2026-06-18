package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hashOrFatal(t *testing.T, dir string, args map[string]string) string {
	t.Helper()
	h, err := computeBuildInputHash(dir, "", "linux/arm64", args)
	if err != nil {
		t.Fatalf("computeBuildInputHash: %v", err)
	}
	return h
}

func TestComputeBuildInputHash_StableAndSensitive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", "FROM python:3.11-slim\nCOPY app.py .\n")
	writeFile(t, dir, "app.py", "print('v1')\n")
	args := map[string]string{"WENDY_DEBUG": "false"}

	base := hashOrFatal(t, dir, args)

	// Identical inputs → identical hash.
	if got := hashOrFatal(t, dir, args); got != base {
		t.Fatalf("hash not stable: %s != %s", got, base)
	}

	// Changing a context file MUST change the hash (no missed change).
	writeFile(t, dir, "app.py", "print('v2')\n")
	if got := hashOrFatal(t, dir, args); got == base {
		t.Fatal("hash unchanged after editing app.py")
	}
}

func TestComputeBuildInputHash_BuildArgsAndDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", "FROM python:3.11-slim\n")
	writeFile(t, dir, "app.py", "print('hi')\n")

	base := hashOrFatal(t, dir, map[string]string{"WENDY_DEBUG": "false"})

	// A different build arg value changes the hash.
	if got := hashOrFatal(t, dir, map[string]string{"WENDY_DEBUG": "true"}); got == base {
		t.Fatal("hash unchanged after build-arg change")
	}

	// A Dockerfile edit changes the hash.
	writeFile(t, dir, "Dockerfile", "FROM python:3.12-slim\n")
	if got := hashOrFatal(t, dir, map[string]string{"WENDY_DEBUG": "false"}); got == base {
		t.Fatal("hash unchanged after Dockerfile change")
	}
}

func TestComputeBuildInputHash_HonorsDockerignore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", "FROM python:3.11-slim\nCOPY app.py .\n")
	writeFile(t, dir, "app.py", "print('hi')\n")
	writeFile(t, dir, ".dockerignore", "*.log\nbuild/\n")
	writeFile(t, dir, "debug.log", "noise\n")
	writeFile(t, dir, "build/out.bin", "artifact\n")

	base := hashOrFatal(t, dir, nil)

	// Changing an ignored file does NOT change the hash (the optimization).
	writeFile(t, dir, "debug.log", "different noise\n")
	if got := hashOrFatal(t, dir, nil); got != base {
		t.Fatal("hash changed after editing a .dockerignore'd file")
	}

	// A file inside an ignored directory is also excluded.
	writeFile(t, dir, "build/out.bin", "rebuilt artifact\n")
	if got := hashOrFatal(t, dir, nil); got != base {
		t.Fatal("hash changed after editing a file in an ignored directory")
	}

	// A non-ignored file still flips the hash.
	writeFile(t, dir, "app.py", "print('changed')\n")
	if got := hashOrFatal(t, dir, nil); got == base {
		t.Fatal("hash unchanged after editing a non-ignored file")
	}
}

func TestDockerIgnoreMatcher(t *testing.T) {
	di := &dockerIgnore{patterns: []string{"node_modules", "*.pyc", "dist", "secrets/key.pem"}}
	cases := []struct {
		path string
		want bool
	}{
		{"node_modules", true},
		{"node_modules/left-pad/index.js", true},
		{"app.pyc", true},
		{"pkg/mod.pyc", true}, // basename glob
		{"dist/bundle.js", true},
		{"secrets/key.pem", true},
		{"app.py", false},
		{"secrets/other.pem", false},
		{"README.md", false},
	}
	for _, c := range cases {
		if got := di.matches(c.path); got != c.want {
			t.Errorf("matches(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestDockerIgnoreNegationIsIgnored(t *testing.T) {
	// Negations re-include files; the conservative matcher ignores them, so the
	// re-included file is simply hashed (safe — never under-excludes).
	dir := t.TempDir()
	writeFile(t, dir, ".dockerignore", "*.log\n!keep.log\n")
	di := loadDockerIgnore(dir)
	if di.matches("keep.log") {
		t.Fatal("negated pattern should not exclude keep.log")
	}
	if !di.matches("other.log") {
		t.Fatal("*.log should still exclude other.log")
	}
}
