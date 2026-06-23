package commands

import "testing"

// TestResolveAgentPlatform covers WDY-1717: a Linux-based agent OS such as
// "wendyos" must normalize to the OCI os "linux" so buildx can resolve public
// base-image manifests (linux/<arch>), while explicit wendy.json platforms and
// darwin/windows are preserved.
func TestResolveAgentPlatform(t *testing.T) {
	tests := []struct {
		name        string
		cfgPlatform string
		agentOS     string
		agentArch   string
		want        string
	}{
		{"wendyos normalizes to linux", "", "wendyos", "arm64", "linux/arm64"},
		{"wendy-lite normalizes to linux", "", "wendy-lite", "arm64", "linux/arm64"},
		{"WendyOS mixed case", "", "WendyOS", "arm64", "linux/arm64"},
		{"linux passes through", "", "linux", "amd64", "linux/amd64"},
		{"empty agent os defaults to linux", "", "", "arm64", "linux/arm64"},
		{"darwin preserved", "", "darwin", "arm64", "darwin/arm64"},
		{"windows preserved", "", "windows", "amd64", "windows/amd64"},
		{"explicit os/arch wins verbatim", "linux/arm64", "wendyos", "arm64", "linux/arm64"},
		{"explicit os-only appends arch", "linux", "wendyos", "arm64", "linux/arm64"},
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
