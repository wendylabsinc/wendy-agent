package commands

import (
	"strings"
	"testing"
)

func TestDetectStorageMediumFromPartitions(t *testing.T) {
	cases := []struct {
		name       string
		deviceType string
		parts      []mountedPartition
		want       string
	}{
		{
			name:       "nvme root device",
			deviceType: "jetson-agx-orin",
			parts:      []mountedPartition{{mountpoint: "/", device: "/dev/nvme0n1p1"}},
			want:       "nvme",
		},
		{
			name:       "emmc root device on agx orin",
			deviceType: "jetson-agx-orin",
			parts:      []mountedPartition{{mountpoint: "/", device: "/dev/mmcblk0p1"}},
			want:       "emmc",
		},
		{
			name:       "mmcblk root device on a pi is an SD card",
			deviceType: "raspberry-pi-5",
			parts:      []mountedPartition{{mountpoint: "/", device: "/dev/mmcblk0p2"}},
			want:       "sd",
		},
		{
			name:       "only the root mount is classified, not other disks",
			deviceType: "jetson-agx-orin",
			parts: []mountedPartition{
				{mountpoint: "/boot", device: "/dev/mmcblk0p11"},
				{mountpoint: "/", device: "/dev/nvme0n1p1"},
				{mountpoint: "/data", device: "/dev/nvme0n1p17"},
			},
			want: "nvme",
		},
		{
			name:       "no root mount is inconclusive",
			deviceType: "jetson-agx-orin",
			parts:      []mountedPartition{{mountpoint: "/data", device: "/dev/nvme0n1p17"}},
			want:       "",
		},
		{
			name:       "overlay root is inconclusive",
			deviceType: "jetson-agx-orin",
			parts:      []mountedPartition{{mountpoint: "/", device: "overlay"}},
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectStorageMediumFromPartitions(tc.deviceType, tc.parts); got != tc.want {
				t.Errorf("detectStorageMediumFromPartitions() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveStorageMedium(t *testing.T) {
	cases := []struct {
		name       string
		deviceType string
		reported   string
		parts      []mountedPartition
		want       string
	}{
		{
			name:       "hardware nvme overrides a stale reported emmc",
			deviceType: "jetson-agx-orin",
			reported:   "emmc",
			parts:      []mountedPartition{{mountpoint: "/", device: "/dev/nvme0n1p1"}},
			want:       "nvme",
		},
		{
			name:       "falls back to reported medium when hardware is inconclusive",
			deviceType: "jetson-orin-nano",
			reported:   "nvme",
			parts:      nil,
			want:       "nvme",
		},
		{
			name:       "agx orin defaults to nvme when nothing is conclusive",
			deviceType: "jetson-agx-orin",
			reported:   "",
			parts:      nil,
			want:       "nvme",
		},
		{
			name:       "legacy agx orin image reporting no medium still resolves to nvme",
			deviceType: "jetson-agx-orin",
			reported:   "",
			parts:      []mountedPartition{{mountpoint: "/", device: "overlay"}},
			want:       "nvme",
		},
		{
			name:       "non-agx device with no signal stays empty (uses default artifact)",
			deviceType: "raspberry-pi-5",
			reported:   "",
			parts:      nil,
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveStorageMedium(tc.deviceType, tc.reported, tc.parts); got != tc.want {
				t.Errorf("resolveStorageMedium() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiskTypeLabel(t *testing.T) {
	cases := map[string]string{
		"nvme":  "NVMe SSD",
		"emmc":  "eMMC",
		"sd":    "SD card",
		"":      "unknown",
		"weird": "unknown",
	}
	for medium, want := range cases {
		if got := diskTypeLabel(medium); got != want {
			t.Errorf("diskTypeLabel(%q) = %q, want %q", medium, got, want)
		}
	}
}

func TestFormatInstallSummary(t *testing.T) {
	got := formatInstallSummary("jetson-agx-orin", "nvme", "orin-1234.local", "WendyOS-0.16.1", "0.17.0")
	t.Logf("summary:\n%s", got)
	for _, want := range []string{"Jetson Agx Orin", "NVMe SSD", "orin-1234.local", "0.16.1", "0.17.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	// The WendyOS- prefix is trimmed so the version transition is consistent.
	if strings.Contains(got, "WendyOS-") {
		t.Errorf("expected WendyOS- prefix to be trimmed:\n%s", got)
	}

	// Target version unknown (local file / --artifact-url): no version arrow.
	noTarget := formatInstallSummary("jetson-agx-orin", "nvme", "orin.local", "WendyOS-0.16.1", "")
	if strings.Contains(noTarget, "→") {
		t.Errorf("expected no version arrow when target is unknown:\n%s", noTarget)
	}
}
