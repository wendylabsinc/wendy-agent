package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveVolumePath(t *testing.T) {
	tmp := t.TempDir()
	old := volumesDir
	volumesDir = tmp
	defer func() { volumesDir = old }()

	root := filepath.Join(tmp, "vol")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		volume  string
		path    string
		wantErr bool
		want    string
	}{
		{"root", "vol", "", false, root},
		{"slash root", "vol", "/", false, root},
		{"nested", "vol", "sub/file.txt", false, filepath.Join(root, "sub/file.txt")},
		{"dotdot escape", "vol", "../../etc/passwd", true, ""},
		{"absolute stays scoped", "vol", "/etc/passwd", false, filepath.Join(root, "etc/passwd")},
		{"empty volume", "", "x", true, ""},
		{"slash in volume", "a/b", "x", true, ""},
		{"dotdot in volume", "..", "x", true, ""},
		{"null byte in volume", "vo\x00l", "x", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveVolumePath(c.volume, c.path)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestResolveVolumePathSymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	old := volumesDir
	volumesDir = tmp
	defer func() { volumesDir = old }()

	root := filepath.Join(tmp, "vol")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the volume that points outside it.
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := resolveVolumePath("vol", "link/secret"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}
