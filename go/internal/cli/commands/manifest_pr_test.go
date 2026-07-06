package commands

import "testing"

func TestPRBasePath(t *testing.T) {
	if got := prBasePath(123); got != "pr/123/" {
		t.Fatalf("prBasePath(123) = %q", got)
	}
}

// TestPRDeviceVersion pins the version-resolution rule that getAvailablePRDevices
// (install path) and getPROTAInfoForDeviceType (update path) must share: prefer
// Latest, falling back to LatestNightly. The publish-pr job always writes PR
// master manifests with --nightly, so every real PR device entry has
// Latest == "" and LatestNightly == "pr-N" — hitting the fallback branch is the
// common case, not an edge case. Before the fix, getAvailablePRDevices read
// dev.Latest directly with no fallback, so every PR device's LatestVersion came
// back "" and installLinuxImage's `if rawVersion == ""  { continue }` filtered
// every device out of the picker, making `install --pr` resolve zero devices.
func TestPRDeviceVersion(t *testing.T) {
	tests := []struct {
		name string
		dev  manifestDevice
		want string
	}{
		{
			name: "nightly-only PR manifest falls back to LatestNightly",
			dev:  manifestDevice{Latest: "", LatestNightly: "pr-123"},
			want: "pr-123",
		},
		{
			name: "Latest takes priority when both are set",
			dev:  manifestDevice{Latest: "0.12.0", LatestNightly: "pr-123"},
			want: "0.12.0",
		},
		{
			name: "neither set resolves to empty",
			dev:  manifestDevice{Latest: "", LatestNightly: ""},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := prDeviceVersion(tc.dev); got != tc.want {
				t.Fatalf("prDeviceVersion(%+v) = %q, want %q", tc.dev, got, tc.want)
			}
		})
	}
}
