package bringup

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/rcm"
)

func TestRunRequiresExactRecoveryHandoffBeforeReadingArtifacts(t *testing.T) {
	const digest = "sha256:52c5b9de12b1f1943a441df0ecdea9f3eb94c7f64b98968855390f14c6e6e05c"
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"missing product", Options{}, "expected Jetson recovery product"},
		{"missing both selectors", Options{ExpectedProduct: rcm.ProductThor}, "identity and USB path are required"},
		{"path only", Options{ExpectedProduct: rcm.ProductThor, DevicePath: "1-2"}, "must be supplied together"},
		{"digest only", Options{ExpectedProduct: rcm.ProductThor, ExpectedECIDDigest: digest}, "must be supplied together"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.Dir = t.TempDir()
			err := Run(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "artifact") {
				t.Fatalf("target validation happened after artifact access: %v", err)
			}
		})
	}
}
