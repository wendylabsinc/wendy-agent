package commands

import (
	"fmt"
	"strings"
)

// agxOrinDeviceType is the OTA manifest key shared by the Jetson AGX Orin NVMe
// and eMMC image variants. Both are published under this one key, distinguished
// only by the reported storage medium (see resolveStorageMedium).
const agxOrinDeviceType = "jetson-agx-orin"

// mountedPartition is a device path and its mountpoint, as reported by the
// agent's device-info partition list. It is the CLI-side, proto-free view of
// agentpb.DiskPartition used to derive the storage medium from real hardware.
type mountedPartition struct {
	mountpoint string
	device     string
}

// detectStorageMediumFromPartitions classifies the device's storage medium from
// the block device backing the root ("/") filesystem — the ground truth for
// "which disk am I running from". Only the root mount is examined: an AGX Orin
// DevKit carries onboard eMMC even when booted from NVMe, so scanning every
// partition would misclassify. Returns "" when the root device is not a
// recognized physical disk (e.g. an overlay) so the caller can fall back.
//
// mmcblk is eMMC on the AGX Orin DevKit but a removable card on SD-booting
// devices (Raspberry Pi, orin-nano SD), so the device type disambiguates it.
func detectStorageMediumFromPartitions(deviceType string, parts []mountedPartition) string {
	rootDev := ""
	for _, p := range parts {
		if p.mountpoint == "/" {
			rootDev = p.device
			break
		}
	}
	if rootDev == "" {
		return ""
	}
	switch {
	case strings.Contains(rootDev, "nvme"):
		return "nvme"
	case strings.Contains(rootDev, "mmcblk"):
		if deviceType == agxOrinDeviceType {
			return "emmc"
		}
		return "sd"
	default:
		return ""
	}
}

// resolveStorageMedium determines the storage medium used to select the OTA
// artifact. Hardware truth (the root block device) wins over the agent's
// self-reported medium, which is derived from /etc/wendyos/device-type and is
// empty or stale on legacy images.
//
// jetson-agx-orin ships both NVMe and eMMC variants under one manifest key,
// where the eMMC image is the top-level default. Every field device is NVMe, so
// when nothing is conclusive we default AGX Orin to nvme "for now" — otherwise a
// device that fails to report its medium is served the eMMC image and rejects
// it (target mismatch). Remove the default once eMMC devices are deployed and
// self-report reliably.
func resolveStorageMedium(deviceType, reported string, parts []mountedPartition) string {
	if m := detectStorageMediumFromPartitions(deviceType, parts); m != "" {
		return m
	}
	if reported != "" {
		return reported
	}
	if deviceType == agxOrinDeviceType {
		return "nvme"
	}
	return ""
}

// diskTypeLabel renders a storage medium as a human-facing disk description.
func diskTypeLabel(medium string) string {
	switch medium {
	case "nvme":
		return "NVMe SSD"
	case "emmc":
		return "eMMC"
	case "sd":
		return "SD card"
	default:
		return "unknown"
	}
}

// formatInstallSummary renders the "what we're installing" block shown before an
// OS update is applied, so the operator can confirm the hardware, disk type,
// target device, and version transition. currentVer and/or targetVer may be
// empty (unknown OS version, or a local/URL artifact whose version is unknown);
// the version line is omitted or shown without an arrow accordingly.
func formatInstallSummary(deviceType, medium, hostname, currentVer, targetVer string) string {
	hardware := humanizeDeviceKey(deviceType)
	if hardware == "" {
		hardware = "unknown"
	}
	// Normalize the OS version display: the agent reports "WendyOS-0.16.1" but
	// manifest target versions are bare ("0.17.0"), so trim the prefix to keep
	// the version transition consistent.
	currentVer = strings.TrimPrefix(currentVer, "WendyOS-")
	targetVer = strings.TrimPrefix(targetVer, "WendyOS-")
	var b strings.Builder
	b.WriteString("Installing WendyOS:\n")
	fmt.Fprintf(&b, "  Hardware: %s\n", hardware)
	fmt.Fprintf(&b, "  Disk:     %s\n", diskTypeLabel(medium))
	if hostname != "" {
		fmt.Fprintf(&b, "  Device:   %s\n", hostname)
	}
	switch {
	case currentVer != "" && targetVer != "":
		fmt.Fprintf(&b, "  Version:  %s → %s\n", currentVer, targetVer)
	case targetVer != "":
		fmt.Fprintf(&b, "  Version:  %s\n", targetVer)
	case currentVer != "":
		fmt.Fprintf(&b, "  Version:  %s (current)\n", currentVer)
	}
	return b.String()
}
