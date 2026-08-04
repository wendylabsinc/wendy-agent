package commands

import "testing"

func TestFormatDarwinOSVersion(t *testing.T) {
	if got := formatDarwinOSVersion("15.6.1\n"); got != "macOS 15.6.1" {
		t.Fatalf("formatDarwinOSVersion() = %q; want %q", got, "macOS 15.6.1")
	}
	if got := formatDarwinOSVersion("  "); got != "" {
		t.Fatalf("formatDarwinOSVersion(empty) = %q; want empty", got)
	}
}

func TestParseLinuxOSRelease(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "pretty name",
			content: "NAME=Ubuntu\nVERSION_ID=24.04\nPRETTY_NAME=\"Ubuntu 24.04.3 LTS\"\n",
			want:    "Ubuntu 24.04.3 LTS",
		},
		{
			name:    "name and version fallback",
			content: "NAME='Arch Linux'\nVERSION_ID=rolling\n",
			want:    "Arch Linux rolling",
		},
		{
			name:    "ignores comments and malformed lines",
			content: "# comment\nnot-a-pair\n",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseLinuxOSRelease([]byte(tt.content)); got != tt.want {
				t.Fatalf("parseLinuxOSRelease() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestFormatWindowsOSVersion(t *testing.T) {
	tests := []struct {
		name           string
		productName    string
		displayVersion string
		buildNumber    string
		want           string
	}{
		{
			name:           "windows 11 compatibility product name",
			productName:    "Windows 10 Pro",
			displayVersion: "24H2",
			buildNumber:    "26100",
			want:           "Windows 11 Pro 24H2 (build 26100)",
		},
		{
			name:           "windows 10",
			productName:    "Windows 10 Enterprise",
			displayVersion: "22H2",
			buildNumber:    "19045",
			want:           "Windows 10 Enterprise 22H2 (build 19045)",
		},
		{
			name:        "minimal fallback",
			buildNumber: "",
			want:        "Windows",
		},
		{
			name:           "windows 11 fallback from build",
			displayVersion: "23H2",
			buildNumber:    "22631",
			want:           "Windows 11 23H2 (build 22631)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatWindowsOSVersion(tt.productName, tt.displayVersion, tt.buildNumber)
			if got != tt.want {
				t.Fatalf("formatWindowsOSVersion() = %q; want %q", got, tt.want)
			}
		})
	}
}
