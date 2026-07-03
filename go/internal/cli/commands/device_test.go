package commands

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestFormatKernelLogRecord(t *testing.T) {
	cases := []struct {
		name string
		rec  *agentpb.KernelLogRecord
		want string
	}{
		{
			name: "sub-second timestamp zero-pads microseconds",
			rec:  &agentpb.KernelLogRecord{TimestampUs: 12_345678, Level: 6, Message: "usb 1-1: new device"},
			want: "[   12.345678] usb 1-1: new device",
		},
		{
			name: "boot instant",
			rec:  &agentpb.KernelLogRecord{TimestampUs: 0, Level: 5, Message: "Linux version 6.1"},
			want: "[    0.000000] Linux version 6.1",
		},
		{
			name: "large uptime keeps full seconds width",
			rec:  &agentpb.KernelLogRecord{TimestampUs: 123456_000007, Level: 4, Message: "late message"},
			want: "[123456.000007] late message",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatKernelLogRecord(tc.rec); got != tc.want {
				t.Errorf("formatKernelLogRecord() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeviceOSLogsFollowFlag(t *testing.T) {
	f := newDeviceOSLogsCmd().Flags().Lookup("follow")
	if f == nil {
		t.Fatal("expected --follow flag on device os-logs command")
	}
	// Follow defaults to true: the kernel dump tails unless explicitly disabled.
	if f.DefValue != "true" {
		t.Errorf("--follow default = %q, want \"true\"", f.DefValue)
	}
	if f.Shorthand != "f" {
		t.Errorf("--follow shorthand = %q, want \"f\"", f.Shorthand)
	}
}

// The kernel ring buffer is now its own `os-logs` command, not a flag on `logs`.
// Guard against the flags drifting back onto `logs`.
func TestDeviceLogsHasNoKernelFlags(t *testing.T) {
	flags := newDeviceLogsCmd().Flags()
	for _, name := range []string{"os", "follow"} {
		if flags.Lookup(name) != nil {
			t.Errorf("device logs should not have --%s flag; use `device os-logs`", name)
		}
	}
}

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

func TestMaybeCheckOSUpdateSkips(t *testing.T) {
	strp := func(s string) *string { return &s }

	tests := []struct {
		name    string
		version *agentpb.GetAgentVersionResponse
	}{
		{"nil version", nil},
		{"non-wendyos darwin", &agentpb.GetAgentVersionResponse{Os: "darwin", OsVersion: strp("14.4")}},
		{"wendyos without an OTA backend",
			&agentpb.GetAgentVersionResponse{Os: "linux", OsVersion: strp("WendyOS-0.10.4"), DeviceType: strp("raspberry-pi-5")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// These inputs must return from the cheap pre-reconnect gate, before
			// any reconnect/manifest/network call. (WendyOS devices with an OTA
			// backend — wendyos-update or mender — do reconnect to re-read the
			// version, so they're not covered here.) A nil connection is safe
			// because the gate returns before it is used.
			outcome, err := maybeCheckOSUpdate(context.Background(), tc.version, nil, false, false, "")
			if err != nil {
				t.Fatalf("maybeCheckOSUpdate() error = %v, want nil", err)
			}
			if outcome.applied || outcome.online {
				t.Fatalf("maybeCheckOSUpdate() outcome = %+v, want zero (skipped)", outcome)
			}
		})
	}
}
