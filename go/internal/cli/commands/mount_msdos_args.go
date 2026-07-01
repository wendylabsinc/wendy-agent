package commands

// This file is intentionally untagged (no _darwin suffix) so the pure
// argument-construction + validation below is unit-testable on any host,
// mirroring disklister_dd_args.go. The darwin-only orchestration that consumes
// it lives in os_provision_darwin.go.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
)

var validDarwinPartitionPath = regexp.MustCompile(`^/dev/disk[0-9]+s[0-9]+$`)

// darwinMountMsdosArgs builds the argv for `mount_msdos`, mounting the FAT
// config partition so the calling (non-root) user owns the mounted view.
//
// macOS mounts FAT volumes on fixed/SSD-backed media (an NVMe SSD, or an SSD in
// a USB enclosure — the Jetson Nano nvme target) respecting ownership, which
// leaves the auto-mount root-owned and makes the non-root writes in
// writeConfigFiles fail with EACCES. -u/-g remap the synthesized owner to the
// caller. vfat carries no on-disk ownership, so the device applies its own uid
// when it boots — this only affects the host-side view. Mirrors the Linux
// uid=/gid= mount fix in os_provision_linux.go.
//
// The result is passed to a privileged `sudo mount_msdos`, so partDev and
// mountPoint are validated defensively, matching darwinDDArgs.
func darwinMountMsdosArgs(uid, gid int, partDev, mountPoint string) ([]string, error) {
	if !validDarwinPartitionPath.MatchString(partDev) {
		return nil, fmt.Errorf("invalid Darwin partition path %q", partDev)
	}
	if uid < 0 || gid < 0 {
		return nil, fmt.Errorf("invalid uid/gid %d/%d", uid, gid)
	}
	if !filepath.IsAbs(mountPoint) {
		return nil, fmt.Errorf("mount point must be an absolute path, got %q", mountPoint)
	}
	return []string{
		"mount_msdos",
		"-u", strconv.Itoa(uid),
		"-g", strconv.Itoa(gid),
		partDev,
		mountPoint,
	}, nil
}
