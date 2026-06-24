package providers

import (
	"context"
	"runtime"
	"testing"
)

func TestDockerProviderShortDisplayName(t *testing.T) {
	p := &DockerProvider{}
	if got := p.DisplayName(); got != "Docker" {
		t.Fatalf("DisplayName() = %q, want \"Docker\"", got)
	}
}

func TestLocalDisplayNameByPlatform(t *testing.T) {
	tests := map[string]string{
		"darwin":  "This Mac",
		"windows": "This PC",
		"linux":   "This PC",
	}
	for goos, want := range tests {
		if got := localDisplayNameFor(goos); got != want {
			t.Fatalf("localDisplayNameFor(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestLocalProviderUsesPlatformDisplayName(t *testing.T) {
	p := &LocalProvider{}
	want := localDisplayNameFor(runtime.GOOS)
	if got := p.DisplayName(); got != want {
		t.Fatalf("DisplayName() = %q, want %q", got, want)
	}

	devices, err := p.DiscoverDevices(context.Background())
	if err != nil || len(devices) != 1 {
		t.Fatalf("DiscoverDevices() = %v, %v; want 1 device", devices, err)
	}
	if devices[0].DisplayName != want {
		t.Fatalf("device DisplayName = %q, want %q", devices[0].DisplayName, want)
	}
}
