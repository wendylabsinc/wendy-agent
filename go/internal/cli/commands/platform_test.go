package commands

import "testing"

func TestResolveAgentPlatform(t *testing.T) {
	tests := []struct {
		name        string
		cfgPlatform string
		agentOS     string
		agentArch   string
		want        string
	}{
		// Empty cfgPlatform: agent OS is normalized to a valid OCI OS. A distro
		// ID reported by a stock-Ubuntu agent must not leak into the platform
		// (WDY-1723).
		{"distro os normalized to linux", "", "ubuntu", "arm64", "linux/arm64"},
		{"debian normalized to linux", "", "debian", "arm64", "linux/arm64"},
		{"rhel normalized to linux", "", "rhel", "amd64", "linux/amd64"},
		{"linux passes through", "", "linux", "arm64", "linux/arm64"},
		{"darwin preserved", "", "darwin", "arm64", "darwin/arm64"},
		{"windows preserved", "", "windows", "amd64", "windows/amd64"},
		{"mixed-case distro normalized", "", "Ubuntu", "arm64", "linux/arm64"},

		// wendy target families ("wendyos"/"wendy-lite") are what `wendy init`
		// writes by default — they are auto-targets, not OCI OS strings, so they
		// must derive from the (normalized) agent OS, not pass through as
		// "wendyos/arm64" (WDY-1723).
		{"wendyos default, wendyos agent", "wendyos", "wendyos", "arm64", "linux/arm64"},
		{"wendyos default, ubuntu agent", "wendyos", "ubuntu", "arm64", "linux/arm64"},
		{"wendyos default, darwin agent", "wendyos", "darwin", "arm64", "darwin/arm64"},
		{"wendy-lite default normalized", "wendy-lite", "ubuntu", "amd64", "linux/amd64"},
		{"mixed-case wendyos normalized", "WendyOS", "ubuntu", "arm64", "linux/arm64"},

		// Explicit full cfgPlatform is trusted as-is (user override / workaround).
		{"explicit full platform as-is", "linux/arm64", "ubuntu", "arm64", "linux/arm64"},
		{"explicit full platform amd64", "linux/amd64", "ubuntu", "arm64", "linux/amd64"},

		// OS-only cfgPlatform: user-specified OS + agent arch.
		{"os-only cfg appends arch", "linux", "ubuntu", "arm64", "linux/arm64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveAgentPlatform(tt.cfgPlatform, tt.agentOS, tt.agentArch); got != tt.want {
				t.Errorf("resolveAgentPlatform(%q, %q, %q) = %q, want %q",
					tt.cfgPlatform, tt.agentOS, tt.agentArch, got, tt.want)
			}
		})
	}
}

func TestOciOS(t *testing.T) {
	tests := map[string]string{
		"ubuntu":  "linux",
		"debian":  "linux",
		"rhel":    "linux",
		"linux":   "linux",
		"":        "linux",
		"darwin":  "darwin",
		"DARWIN":  "darwin",
		"windows": "windows",
	}
	for in, want := range tests {
		if got := ociOS(in); got != want {
			t.Errorf("ociOS(%q) = %q, want %q", in, got, want)
		}
	}
}
