//go:build darwin || linux || windows

package commands

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestExitErrWithStderr(t *testing.T) {
	t.Run("plain error passes through", func(t *testing.T) {
		err := errors.New("boom")
		if got := exitErrWithStderr(err); got != err {
			t.Fatalf("got %v; want original error", got)
		}
	})

	t.Run("exit error with stderr is enriched", func(t *testing.T) {
		// Real ExitError with captured stderr, the same shape exec.Output()
		// produces on command failure.
		_, err := exec.Command("sh", "-c", "echo 'no WiFi device found' >&2; exit 1").Output()
		if err == nil {
			t.Skip("sh unavailable on this platform")
		}
		got := exitErrWithStderr(err)
		if !strings.Contains(got.Error(), "no WiFi device found") {
			t.Fatalf("error %q should contain captured stderr", got.Error())
		}
		var ee *exec.ExitError
		if !errors.As(got, &ee) {
			t.Fatalf("enriched error should still wrap the ExitError, got %T", got)
		}
	})

	t.Run("exit error with empty stderr passes through", func(t *testing.T) {
		_, err := exec.Command("sh", "-c", "exit 1").Output()
		if err == nil {
			t.Skip("sh unavailable on this platform")
		}
		if got := exitErrWithStderr(err); got != err {
			t.Fatalf("got %v; want original error", got)
		}
	})
}

func TestWifiScanFailureNotice(t *testing.T) {
	tests := []struct {
		name    string
		scanErr error
		want    string
	}{
		{"no adapter", errNoWifiAdapter, "No WiFi adapter detected on this machine."},
		{"wrapped no adapter", fmt.Errorf("scan: %w", errNoWifiAdapter), "No WiFi adapter detected on this machine."},
		{"generic failure", errors.New("exit status 1: something broke"), "WiFi scan failed: exit status 1: something broke"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wifiScanFailureNotice(tc.scanErr); got != tc.want {
				t.Fatalf("got %q; want %q", got, tc.want)
			}
		})
	}

	t.Run("empty scan result", func(t *testing.T) {
		got := wifiScanFailureNotice(nil)
		if !strings.HasPrefix(got, "No WiFi networks found.") {
			t.Fatalf("got %q; want 'No WiFi networks found.' prefix", got)
		}
	})
}
