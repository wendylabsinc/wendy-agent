//go:build linux

package services

import (
	"os"
	"path/filepath"
	"testing"
)

// tempFile writes content to a named file in a temp dir and returns the path.
func tempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseOSRelease(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantID      string
		wantVersion string
		wantOK      bool
	}{
		{
			name:        "ubuntu 22.04 with quotes",
			content:     "ID=\"ubuntu\"\nVERSION_ID=\"22.04\"\nNAME=\"Ubuntu\"\n",
			wantID:      "ubuntu",
			wantVersion: "22.04",
			wantOK:      true,
		},
		{
			name:        "arch linux no version",
			content:     "ID=arch\nNAME=\"Arch Linux\"\n",
			wantID:      "arch",
			wantVersion: "",
			wantOK:      true,
		},
		{
			name:        "debian 12",
			content:     "ID=debian\nVERSION_ID=\"12\"\n",
			wantID:      "debian",
			wantVersion: "12",
			wantOK:      true,
		},
		{
			name:        "rhel 8 via os-release",
			content:     "ID=\"rhel\"\nVERSION_ID=\"8.6\"\n",
			wantID:      "rhel",
			wantVersion: "8.6",
			wantOK:      true,
		},
		{
			name:    "empty file",
			content: "\n",
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tempFile(t, "os-release", tt.content)
			id, version, ok := parseOSRelease(path)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v; want %v", ok, tt.wantOK)
			}
			if id != tt.wantID {
				t.Errorf("id = %q; want %q", id, tt.wantID)
			}
			if version != tt.wantVersion {
				t.Errorf("version = %q; want %q", version, tt.wantVersion)
			}
		})
	}
}

func TestParseRedHatRelease(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantID      string
		wantVersion string
		wantOK      bool
	}{
		{
			name:        "rhel 8",
			content:     "Red Hat Enterprise Linux release 8.6 (Ootpa)\n",
			wantID:      "rhel",
			wantVersion: "8.6",
			wantOK:      true,
		},
		{
			name:        "centos 7",
			content:     "CentOS Linux release 7.9.2009 (Core)\n",
			wantID:      "centos",
			wantVersion: "7.9.2009",
			wantOK:      true,
		},
		{
			name:        "fedora 38",
			content:     "Fedora release 38 (Thirty Eight)\n",
			wantID:      "fedora",
			wantVersion: "38",
			wantOK:      true,
		},
		{
			name:    "unrecognised content",
			content: "Something else entirely\n",
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tempFile(t, "redhat-release", tt.content)
			id, version, ok := parseRedHatRelease(path)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v; want %v", ok, tt.wantOK)
			}
			if id != tt.wantID {
				t.Errorf("id = %q; want %q", id, tt.wantID)
			}
			if version != tt.wantVersion {
				t.Errorf("version = %q; want %q", version, tt.wantVersion)
			}
		})
	}
}

func TestParseDebianVersion(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantVersion string
		wantOK      bool
	}{
		{
			name:        "debian 11",
			content:     "11.5\n",
			wantVersion: "11.5",
			wantOK:      true,
		},
		{
			name:    "empty",
			content: "  \n",
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tempFile(t, "debian_version", tt.content)
			version, ok := parseDebianVersion(path)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v; want %v", ok, tt.wantOK)
			}
			if version != tt.wantVersion {
				t.Errorf("version = %q; want %q", version, tt.wantVersion)
			}
		})
	}
}
