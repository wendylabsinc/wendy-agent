//go:build darwin

package commands

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/wendyconf"
)

// configPartitionSupported reports whether writeConfigPartition has a working
// implementation on this OS. Callers gate the agent download + config write
// on this so non-supported platforms don't pay the network cost just to fail.
const configPartitionSupported = true

// writeConfigPartition finds, mounts, populates, and unmounts the FAT32 config
// partition on d after a dd write. agentBinary is the arm64 agent binary
// content. creds and deviceName are written to wendy.conf when non-empty.
func writeConfigPartition(d drive, agentBinary []byte, creds []wendyconf.WifiCredential, deviceName string, provisioningJSON []byte) error {
	partDev, err := findConfigPartition(d.DevicePath)
	if err != nil {
		return fmt.Errorf("locating config partition on %s: %w", d.DevicePath, err)
	}

	mountPoint, unmount, err := mountConfigPartition(partDev)
	if err != nil {
		return fmt.Errorf("mounting config partition %s: %w", partDev, err)
	}
	defer unmount()

	return writeConfigFiles(mountPoint, agentBinary, creds, deviceName, provisioningJSON)
}

// findConfigPartition runs `diskutil list <diskDev>` (which also rescans the
// partition table after dd) and returns the device node for the partition
// labelled "config".
func findConfigPartition(diskDev string) (string, error) {
	out, err := exec.Command("diskutil", "list", diskDev).Output()
	if err != nil {
		return "", fmt.Errorf("diskutil list %s: %w", diskDev, err)
	}

	// diskutil list output contains lines like:
	//    2:  Microsoft Basic Data  config      67.1 MB    disk4s2
	// We look for a field equal to "config" and take the last field as the
	// partition device (without the /dev/ prefix).
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if strings.EqualFold(f, "config") && i > 0 {
				last := fields[len(fields)-1]
				if strings.HasPrefix(last, "disk") {
					return "/dev/" + last, nil
				}
			}
		}
	}
	return "", fmt.Errorf("config partition not found on %s (is the image fully written?)", diskDev)
}

// mountConfigPartition mounts the FAT32 config partition at a private temp
// directory owned by the calling user and returns the mount point plus an
// unmount/cleanup func to defer.
//
// macOS auto-mounts FAT volumes when the partition table is rescanned (by the
// `diskutil list` in findConfigPartition). On removable SD cards that auto-mount
// ignores ownership and is writable, but on fixed/SSD-backed media (an NVMe SSD,
// or an SSD in a USB enclosure — the Jetson Nano nvme target) macOS respects
// ownership and mounts the volume root-owned, so the non-root WriteFile calls in
// writeConfigFiles fail with EACCES. We drop any such auto-mount, then re-mount
// via `sudo mount_msdos -u/-g` so the caller owns the files. vfat has no on-disk
// ownership; the device applies its own uid at boot, so this only affects the
// host-side view. Mirrors the Linux uid=/gid= fix in os_provision_linux.go.
func mountConfigPartition(partDev string) (string, func(), error) {
	// Drop any auto-mount (possibly root-owned) so mount_msdos can take the device.
	exec.Command("diskutil", "unmount", partDev).Run() //nolint:errcheck

	tmpDir, err := os.MkdirTemp("", "wendyos-config-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp mount dir: %w", err)
	}

	args, err := darwinMountMsdosArgs(os.Getuid(), os.Getgid(), partDev, tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir) //nolint:errcheck
		return "", nil, err
	}

	if out, err := exec.Command("sudo", args...).CombinedOutput(); err != nil {
		os.RemoveAll(tmpDir) //nolint:errcheck
		return "", nil, fmt.Errorf("mount_msdos %s: %s: %w", partDev, strings.TrimSpace(string(out)), err)
	}

	unmount := func() {
		exec.Command("sudo", "umount", tmpDir).Run() //nolint:errcheck
		os.RemoveAll(tmpDir)                         //nolint:errcheck
	}
	return tmpDir, unmount, nil
}
