//go:build darwin

package commands

import (
	"strings"
	"testing"
)

// The elevated cp in sudoDirTarget.WriteFile runs as root, so anything that is
// not a bare file name under an absolute mount point must be refused before
// sudo is ever invoked. These inputs all fail validation up front — no staging
// file is written and no command runs, so the test needs no hardware or sudo.
func TestSudoDirTargetRejectsUnsafeDestinations(t *testing.T) {
	tests := []struct {
		name    string
		target  sudoDirTarget
		file    string
		wantErr string
	}{
		{
			name:    "path separator in name",
			target:  sudoDirTarget("/Volumes/config"),
			file:    "subdir/wendy.conf",
			wantErr: "not a bare file name",
		},
		{
			name:    "parent traversal in name",
			target:  sudoDirTarget("/Volumes/config"),
			file:    "../wendy.conf",
			wantErr: "not a bare file name",
		},
		{
			name:    "bare parent traversal",
			target:  sudoDirTarget("/Volumes/config"),
			file:    "..",
			wantErr: "not a bare file name",
		},
		{
			name:    "absolute name",
			target:  sudoDirTarget("/Volumes/config"),
			file:    "/etc/sudoers.d/wendy",
			wantErr: "not a bare file name",
		},
		{
			name:    "empty name",
			target:  sudoDirTarget("/Volumes/config"),
			file:    "",
			wantErr: "not a bare file name",
		},
		{
			name:    "relative mount point",
			target:  sudoDirTarget("Volumes/config"),
			file:    "wendy.conf",
			wantErr: "not an absolute path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.target.WriteFile(tt.file, []byte("data"), 0o644)
			if err == nil {
				t.Fatalf("WriteFile(%q) on %q succeeded, want error", tt.file, string(tt.target))
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("WriteFile(%q) error = %q, want it to contain %q", tt.file, err, tt.wantErr)
			}
		})
	}
}

// findConfigPartition only ever returns /dev/ nodes whose tail matches
// darwinPartitionRe, so diskutil and the elevated writes never see a
// malformed device path scraped out of `diskutil list` output.
func TestDarwinPartitionRe(t *testing.T) {
	valid := []string{"disk4s2", "disk11s2", "disk0s1"}
	for _, v := range valid {
		if !darwinPartitionRe.MatchString(v) {
			t.Errorf("darwinPartitionRe rejected valid partition %q", v)
		}
	}

	invalid := []string{
		"disk4",         // whole disk, not a partition
		"diskXs2",       // non-numeric disk
		"disk4s",        // missing slice number
		"disk4s2s1",     // APFS snapshot-style suffix, never a FAT partition
		"disk4s2/../..", // traversal
		" disk4s2",      // leading junk
		"disk4s2 ",      // trailing junk
	}
	for _, v := range invalid {
		if darwinPartitionRe.MatchString(v) {
			t.Errorf("darwinPartitionRe accepted malformed partition %q", v)
		}
	}
}
