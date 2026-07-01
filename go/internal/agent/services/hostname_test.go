package services

import (
	"strings"
	"testing"
)

func TestValidHostname(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"simple", "wendy", true},
		{"with prefix", "wendyos-living-room", true},
		{"single letter", "a", true},
		{"letter then digit", "a1", true},
		{"hyphens and digits", "x-1-y-2", true},
		{"max length 63", strings.Repeat("a", 63), true},

		{"empty", "", false},
		{"too long 64", strings.Repeat("a", 64), false},
		{"starts with digit", "1wendy", false},
		{"starts with hyphen", "-wendy", false},
		{"ends with hyphen", "wendy-", false},
		{"uppercase", "Wendy", false},
		{"underscore", "wendy_os", false},
		{"dot", "wendy.os", false},
		{"space", "wendy os", false},
		{"trailing newline", "wendy\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validHostname(tt.in); got != tt.want {
				t.Errorf("validHostname(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
