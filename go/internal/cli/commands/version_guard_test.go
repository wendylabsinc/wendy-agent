package commands

import "testing"

// TestAgentBehindCLI / TestCLIBehindAgent pin the dev-aware "is one side behind
// the other?" predicates used by discover/device version messaging. Dev builds
// on either side (the literal "dev" or a "-dev" suffix) are treated as the
// latest version, so neither side is ever reported as behind (WDY-1770).
func TestAgentBehindCLI(t *testing.T) {
	tests := []struct {
		cli, agent string
		want       bool
	}{
		{"0.11.0", "0.10.0", true},  // agent genuinely behind
		{"0.10.0", "0.11.0", false}, // agent ahead
		{"0.10.0", "0.10.0", false}, // equal
		{"dev", "0.10.0", false},    // dev CLI never flags real agents
		{"2026.06.30-1-dev", "0.10.0", false},
		{"0.11.0", "dev", false}, // dev agent never flagged
		{"0.11.0", "2026.06.30-1-dev", false},
		{"0.11.0", "", false}, // unknown agent version
	}
	for _, tt := range tests {
		if got := agentBehindCLI(tt.cli, tt.agent); got != tt.want {
			t.Errorf("agentBehindCLI(%q, %q) = %v, want %v", tt.cli, tt.agent, got, tt.want)
		}
	}
}

func TestCLIBehindAgent(t *testing.T) {
	tests := []struct {
		cli, agent string
		want       bool
	}{
		{"0.10.0", "0.11.0", true},  // CLI genuinely behind
		{"0.11.0", "0.10.0", false}, // CLI ahead
		{"0.10.0", "0.10.0", false}, // equal
		{"dev", "0.11.0", false},    // dev CLI is never behind
		{"2026.06.30-1-dev", "0.11.0", false},
		{"0.10.0", "dev", false}, // dev agent never makes the CLI look behind
		{"0.10.0", "", false},    // unknown agent version
	}
	for _, tt := range tests {
		if got := cliBehindAgent(tt.cli, tt.agent); got != tt.want {
			t.Errorf("cliBehindAgent(%q, %q) = %v, want %v", tt.cli, tt.agent, got, tt.want)
		}
	}
}
