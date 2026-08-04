package commands

import (
	"strings"
	"testing"
)

func TestValidateUnitreeG1Host(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		goos    string
		goarch  string
		wantErr bool
	}{
		{name: "x86-64 Linux", goos: "linux", goarch: "amd64"},
		{name: "ARM Linux", goos: "linux", goarch: "arm64", wantErr: true},
		{name: "Intel macOS", goos: "darwin", goarch: "amd64", wantErr: true},
		{name: "Apple Silicon macOS", goos: "darwin", goarch: "arm64", wantErr: true},
		{name: "Windows", goos: "windows", goarch: "amd64", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUnitreeG1Host(tt.goos, tt.goarch)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("validateUnitreeG1Host() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateUnitreeG1Host() error = nil, want unsupported-host error")
			}
			for _, want := range []string{unitreeG1HostRequirement, tt.goos + "/" + tt.goarch} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}
