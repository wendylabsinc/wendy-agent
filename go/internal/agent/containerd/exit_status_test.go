package containerd

import (
	"errors"
	"testing"
)

func TestClassifyExit(t *testing.T) {
	tests := []struct {
		name string
		code uint32
		oom  bool
		want string
	}{
		{"clean", 0, false, exitReasonExited},
		{"nonzero", 1, false, exitReasonCrashed},
		{"sigkill", 137, false, exitReasonCrashed},
		{"oom overrides crash", 137, true, exitReasonOOMKilled},
		{"oom overrides clean", 0, true, exitReasonOOMKilled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyExit(tc.code, tc.oom); got != tc.want {
				t.Fatalf("classifyExit(%d, %v) = %q, want %q", tc.code, tc.oom, got, tc.want)
			}
		})
	}
}

func TestClassifyStartError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"applying entitlements", errors.New("applying entitlements: host-admin denied"), exitReasonEntitlementDenied},
		{"entitlement requires (bluetooth)", errors.New("the bluetooth entitlement requires xdg-dbus-proxy"), exitReasonEntitlementDenied},
		{"case-insensitive marker", errors.New("Applying Entitlements: nope"), exitReasonEntitlementDenied},
		// False-positive guard: the bare word "entitlement" in an unrelated
		// failure (e.g. an image name) must NOT be classified as a denial.
		{"image named entitlement", errors.New("creating task: pulling registry.example.com/entitlement-manager: not found"), exitReasonStartFailed},
		{"generic image error", errors.New("creating task: image not found"), exitReasonStartFailed},
		{"nil", nil, exitReasonStartFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyStartError(tc.err); got != tc.want {
				t.Fatalf("classifyStartError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestParseExitLabels(t *testing.T) {
	t.Run("absent reason -> not ok", func(t *testing.T) {
		if _, _, ok := parseExitLabels(map[string]string{}); ok {
			t.Fatal("expected ok=false for empty labels")
		}
		// Code without reason is still not a recorded exit.
		if _, _, ok := parseExitLabels(map[string]string{labelKeyExitCode: "5"}); ok {
			t.Fatal("expected ok=false when only the code label is present")
		}
	})
	t.Run("reason + code", func(t *testing.T) {
		code, reason, ok := parseExitLabels(map[string]string{
			labelKeyExitReason: exitReasonCrashed,
			labelKeyExitCode:   "1",
		})
		if !ok || reason != exitReasonCrashed || code != 1 {
			t.Fatalf("got (%d, %q, %v), want (1, crashed, true)", code, reason, ok)
		}
	})
	t.Run("reason without code", func(t *testing.T) {
		code, reason, ok := parseExitLabels(map[string]string{labelKeyExitReason: exitReasonOOMKilled})
		if !ok || reason != exitReasonOOMKilled || code != 0 {
			t.Fatalf("got (%d, %q, %v), want (0, oom_killed, true)", code, reason, ok)
		}
	})
	t.Run("start failure sentinel", func(t *testing.T) {
		code, _, ok := parseExitLabels(map[string]string{
			labelKeyExitReason: exitReasonStartFailed,
			labelKeyExitCode:   "-1",
		})
		if !ok || code != exitCodeDidNotStart {
			t.Fatalf("got (%d, ok=%v), want (-1, true)", code, ok)
		}
	})
}
