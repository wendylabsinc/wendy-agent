package commands

import (
	"testing"
)

func TestDeviceCmd_HasPs(t *testing.T) {
	cmd := newDeviceCmd()
	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Use == "ps" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'ps' subcommand on device command")
	}
}

func TestDefaultEnrollmentName(t *testing.T) {
	cases := map[string]string{
		"playful-reed.local": "playful-reed",
		"playful-reed":       "playful-reed",
		"192.168.1.50":       "",
		"":                   "",
	}
	for in, want := range cases {
		if got := defaultEnrollmentName(in); got != want {
			t.Errorf("defaultEnrollmentName(%q) = %q, want %q", in, got, want)
		}
	}
}
