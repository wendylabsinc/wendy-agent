package t234

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// openFixture decompresses a gzipped ext4 fixture (built with mke2fs -d from
// a known tree; see testdata) into memory.
func openFixture(t *testing.T, name string) *bytes.Reader {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("opening fixture: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompressing fixture: %v", err)
	}
	return bytes.NewReader(data)
}

func TestExt4ReadFile(t *testing.T) {
	for _, fixture := range []string{"flashpkg-1k.ext4.gz", "flashpkg-4k.ext4.gz"} {
		t.Run(fixture, func(t *testing.T) {
			img := openFixture(t, fixture)

			status, err := Ext4ReadFile(img, "flashpkg/status")
			if err != nil {
				t.Fatalf("reading status: %v", err)
			}
			if got := string(status); got != "SUCCESS\n" {
				t.Errorf("status = %q, want SUCCESS", got)
			}

			seq, err := Ext4ReadFile(img, "flashpkg/conf/command_sequence")
			if err != nil {
				t.Fatalf("reading command_sequence: %v", err)
			}
			if got := string(seq); got != "bootloader\nreboot\n" {
				t.Errorf("command_sequence = %q", got)
			}

			// A multi-block file exercises the extent walk.
			big, err := Ext4ReadFile(img, "flashpkg/logs/big.log")
			if err != nil {
				t.Fatalf("reading big.log: %v", err)
			}
			if len(big) != 300000 {
				t.Errorf("big.log length = %d, want 300000", len(big))
			}

			if _, err := Ext4ReadFile(img, "flashpkg/nope"); err == nil {
				t.Errorf("expected error for missing file")
			}
			if _, err := Ext4ReadFile(img, "flashpkg/logs"); err == nil {
				t.Errorf("expected error reading a directory as a file")
			}
		})
	}
}

func TestExt4ListDir(t *testing.T) {
	img := openFixture(t, "flashpkg-4k.ext4.gz")
	names, err := Ext4ListDir(img, "flashpkg/logs")
	if err != nil {
		t.Fatalf("listing logs: %v", err)
	}
	want := map[string]bool{"bootloader.log": true, "big.log": true}
	if len(names) != len(want) {
		t.Fatalf("got %v, want the %d known logs", names, len(want))
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected entry %q", n)
		}
	}
}

func TestExt4NotAFilesystem(t *testing.T) {
	if _, err := Ext4ReadFile(bytes.NewReader(make([]byte, 4096)), "x"); err == nil {
		t.Fatal("expected error for a non-ext4 image")
	}
}
