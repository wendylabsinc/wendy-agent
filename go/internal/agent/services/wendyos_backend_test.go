package services

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/oshealth"
)

func TestParseWendyOSProgress(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantPhase   string
		wantPercent int32
		wantOK      bool
	}{
		{
			name:        "well-formed progress line",
			line:        `{"phase":"write","percent":42,"msg":"writing slot"}`,
			wantPhase:   "write",
			wantPercent: 42,
			wantOK:      true,
		},
		{
			name:        "commit phase at 100",
			line:        `{"phase":"commit","percent":100}`,
			wantPhase:   "commit",
			wantPercent: 100,
			wantOK:      true,
		},
		{
			name:   "non-JSON line is ignored",
			line:   "wendyos-update: writing artifact",
			wantOK: false,
		},
		{
			name:   "JSON without a phase is ignored",
			line:   `{"current_slot":"a","pending":false}`,
			wantOK: false,
		},
		{
			name:   "blank line is ignored",
			line:   "   ",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			phase, percent, ok := parseWendyOSProgress(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parseWendyOSProgress(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if phase != tt.wantPhase || percent != tt.wantPercent {
				t.Fatalf("parseWendyOSProgress(%q) = (%q, %d), want (%q, %d)",
					tt.line, phase, percent, tt.wantPhase, tt.wantPercent)
			}
		})
	}
}

func TestCommitStatusForExitCode(t *testing.T) {
	tests := []struct {
		code int
		want oshealth.MenderStatus
	}{
		{code: 0, want: oshealth.MenderOK},
		{code: 2, want: oshealth.MenderNothingPending}, // mirrors mender-update
		{code: 1, want: oshealth.MenderError},
		{code: 4, want: oshealth.MenderError}, // verify failed at commit
	}
	for _, tt := range tests {
		if got := commitStatusForExitCode(tt.code); got != tt.want {
			t.Errorf("commitStatusForExitCode(%d) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestWendyOSInstallErrorMessage(t *testing.T) {
	tail := []string{"wendyos-update: validating artifact", "wendyos-update: checksum mismatch"}

	tests := []struct {
		name        string
		exitCode    int
		wantContain string
	}{
		{name: "artifact rejected", exitCode: 3, wantContain: "rejected"},
		{name: "generic error", exitCode: 1, wantContain: "wendyos-update install failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := wendyOSInstallErrorMessage(tt.exitCode, tail)
			if !strings.Contains(strings.ToLower(msg), strings.ToLower(tt.wantContain)) {
				t.Fatalf("wendyOSInstallErrorMessage(%d) = %q, want it to contain %q",
					tt.exitCode, msg, tt.wantContain)
			}
			// The captured tail must be surfaced so failures are diagnosable.
			if !strings.Contains(msg, "checksum mismatch") {
				t.Fatalf("wendyOSInstallErrorMessage(%d) = %q, want it to include the output tail", tt.exitCode, msg)
			}
		})
	}
}

// exit 4 (verify failed) is a commit-time code in the wendyos-update contract;
// install never emits it. The install error mapper must therefore not mislabel
// a stray exit 4 as a verification failure — it falls through to the generic
// message so it is not confused with a real install-time verify code.
func TestWendyOSInstallErrorMessageDoesNotClaimVerifyFailure(t *testing.T) {
	msg := wendyOSInstallErrorMessage(4, nil)
	if strings.Contains(strings.ToLower(msg), "verif") {
		t.Fatalf("wendyOSInstallErrorMessage(4) = %q, want no verification-failure wording (exit 4 is commit-time)", msg)
	}
	if !strings.Contains(msg, "wendyos-update install failed") {
		t.Fatalf("wendyOSInstallErrorMessage(4) = %q, want the generic install-failed message", msg)
	}
}
