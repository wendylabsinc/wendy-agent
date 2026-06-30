package mount

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultMountpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mp, err := DefaultMountpoint("mydevice", "data")
	if err != nil {
		t.Fatalf("DefaultMountpoint: %v", err)
	}
	want := filepath.Join(home, "Wendy", "mydevice", "data")
	if mp != want {
		t.Fatalf("got %q want %q", mp, want)
	}
	if !strings.Contains(mp, "Wendy") {
		t.Fatal("expected Wendy in path")
	}
	if _, err := os.Stat(mp); err != nil {
		t.Fatalf("mountpoint not created: %v", err)
	}
}
