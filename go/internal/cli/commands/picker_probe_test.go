package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

func TestNextProbeState(t *testing.T) {
	cases := []struct {
		name                     string
		existing, incoming, want tui.ProbeState
	}{
		{"first probe starts pending", tui.ProbeNone, tui.ProbePending, tui.ProbePending},
		{"pending resolves to ok", tui.ProbePending, tui.ProbeOK, tui.ProbeOK},
		{"pending resolves to failed", tui.ProbePending, tui.ProbeFailed, tui.ProbeFailed},
		{"ok survives transient failure", tui.ProbeOK, tui.ProbeFailed, tui.ProbeOK},
		{"ok not reset to pending on rediscovery", tui.ProbeOK, tui.ProbePending, tui.ProbeOK},
		{"failed recovers on retry success", tui.ProbeFailed, tui.ProbeOK, tui.ProbeOK},
		{"failed not flipped to spinner on rediscovery", tui.ProbeFailed, tui.ProbePending, tui.ProbeFailed},
		{"none stays none", tui.ProbeNone, tui.ProbeNone, tui.ProbeNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextProbeState(tc.existing, tc.incoming); got != tc.want {
				t.Errorf("nextProbeState(%v,%v) = %v; want %v", tc.existing, tc.incoming, got, tc.want)
			}
		})
	}
}
