package commands

import (
	"context"
	"strings"
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

func TestConflictingOSFlags(t *testing.T) {
	changed := map[string]bool{"app": true, "tail": true, "service": false}
	got := conflictingOSFlags(func(name string) bool { return changed[name] })
	// Order follows kernelLogConflictFlags declaration order.
	want := []string{"app", "tail"}
	if len(got) != len(want) {
		t.Fatalf("conflictingOSFlags() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("conflictingOSFlags() = %v, want %v", got, want)
		}
	}

	if none := conflictingOSFlags(func(string) bool { return false }); len(none) != 0 {
		t.Errorf("expected no conflicts when nothing changed, got %v", none)
	}
}

func TestDeviceLogsFollowFlag(t *testing.T) {
	f := newDeviceLogsCmd().Flags().Lookup("follow")
	if f == nil {
		t.Fatal("expected --follow flag on device logs command")
	}
	// Follow defaults to true: the kernel dump tails unless explicitly disabled.
	if f.DefValue != "true" {
		t.Errorf("--follow default = %q, want \"true\"", f.DefValue)
	}
	if f.Shorthand != "f" {
		t.Errorf("--follow shorthand = %q, want \"f\"", f.Shorthand)
	}

	// --follow only governs --os; using it for container logs must error rather
	// than silently do nothing.
	cmd := newDeviceLogsCmd()
	cmd.SetArgs([]string{"--follow=false"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--follow only applies to --os") {
		t.Fatalf("expected --follow-without-os error, got %v", err)
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
		{"wendyos without mender",
			&agentpb.GetAgentVersionResponse{Os: "linux", OsVersion: strp("WendyOS-0.10.4"), DeviceType: strp("raspberry-pi-5")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// These inputs must return from the cheap pre-reconnect gate, before
			// any reconnect/manifest/network call. (WendyOS+mender devices do
			// reconnect to re-read the version, so they're not covered here.)
			// A nil connection is safe because the gate returns before it is used.
			if err := maybeCheckOSUpdate(context.Background(), tc.version, nil, false, false, ""); err != nil {
				t.Fatalf("maybeCheckOSUpdate() error = %v, want nil", err)
			}
		})
	}
}
