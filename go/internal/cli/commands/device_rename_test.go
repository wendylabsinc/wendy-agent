package commands

import (
	"strings"
	"testing"
)

func TestValidateHostnameArg(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"simple", "wendy", false},
		{"with prefix", "wendyos-living-room", false},
		{"single letter", "a", false},
		{"digits and hyphens", "node-1-of-3", false},
		{"max length 63", strings.Repeat("a", 63), false},

		{"empty", "", true},
		{"too long 64", strings.Repeat("a", 64), true},
		{"starts with digit", "1wendy", true},
		{"starts with hyphen", "-wendy", true},
		{"ends with hyphen", "wendy-", true},
		{"uppercase", "Wendy", true},
		{"underscore", "wendy_os", true},
		{"dot", "wendy.os", true},
		{"space", "wendy os", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHostnameArg(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateHostnameArg(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestResolveRenameNameFromArg(t *testing.T) {
	got, err := resolveRenameName([]string{"  living-room  "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "living-room" {
		t.Errorf("resolveRenameName trimmed = %q, want %q", got, "living-room")
	}

	if _, err := resolveRenameName([]string{"Bad_Name"}); err == nil {
		t.Errorf("resolveRenameName with invalid arg: expected error, got nil")
	}
}
