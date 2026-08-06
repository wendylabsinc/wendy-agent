package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

func TestCloudDeviceInfoIncludesID(t *testing.T) {
	a := &cloudpb.Asset{Id: 77}
	info := cloudDeviceInfoFromAsset(a, nil)
	if info.ID != 77 {
		t.Fatalf("expected ID 77, got %d", info.ID)
	}
}

func TestCloudDeviceInfoFallsBackToOSTypeFromAsset(t *testing.T) {
	osType, arch := "darwin", "arm64"
	a := &cloudpb.Asset{Id: 1, OsType: &osType, Architecture: &arch}
	info := cloudDeviceInfoFromAsset(a, nil)
	if info.Type != "macOS (arm64)" {
		t.Fatalf("expected %q, got %q", "macOS (arm64)", info.Type)
	}
}

func TestCloudDeviceInfoFallsBackToOSTypeFromAgentVersion(t *testing.T) {
	a := &cloudpb.Asset{Id: 1}
	ver := &agentpb.GetAgentVersionResponse{Os: "ubuntu", CpuArchitecture: "amd64"}
	info := cloudDeviceInfoFromAsset(a, ver)
	if info.Type != "Linux (x86_64)" {
		t.Fatalf("expected %q, got %q", "Linux (x86_64)", info.Type)
	}
}

func TestCloudDeviceInfoPrefersDeviceTypeOverOSType(t *testing.T) {
	deviceType, osType := "raspberry-pi-5", "wendyos"
	a := &cloudpb.Asset{Id: 1, DeviceType: &deviceType, OsType: &osType}
	info := cloudDeviceInfoFromAsset(a, nil)
	if info.Type != "Raspberry Pi 5" {
		t.Fatalf("expected %q, got %q", "Raspberry Pi 5", info.Type)
	}
}

func TestHumanReadableOSType(t *testing.T) {
	cases := []struct {
		os, arch, want string
	}{
		{"darwin", "arm64", "macOS (arm64)"},
		{"darwin", "", "macOS"},
		{"ubuntu", "amd64", "Linux (x86_64)"},
		{"linux", "aarch64", "Linux (arm64)"},
		{"", "arm64", ""},
	}
	for _, c := range cases {
		if got := humanReadableOSType(c.os, c.arch); got != c.want {
			t.Errorf("humanReadableOSType(%q, %q) = %q, want %q", c.os, c.arch, got, c.want)
		}
	}
}
