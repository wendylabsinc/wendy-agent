package commands

import (
	"slices"
	"strconv"
	"testing"
)

// Without -u/-g, mount_msdos synthesizes uid=0 for every file on the FAT
// config partition, so the subsequent non-root WriteFile calls in
// writeConfigFiles fail with EACCES (the reported "open /Volumes/config/
// wendy-agent: permission denied"). This is the macOS analog of the Linux
// uid=/gid= fix. Lock in that the caller's ownership is always mapped.
func TestDarwinMountMsdosArgsMapsOwnershipToCaller(t *testing.T) {
	const uid, gid = 501, 20
	args, err := darwinMountMsdosArgs(uid, gid, "/dev/disk4s2", "/tmp/wendyos-config-xyz")
	if err != nil {
		t.Fatalf("darwinMountMsdosArgs() error = %v", err)
	}

	if len(args) == 0 || args[0] != "mount_msdos" {
		t.Fatalf("darwinMountMsdosArgs() = %v; want mount_msdos as argv[0]", args)
	}

	if i := slices.Index(args, "-u"); i < 0 || i+1 >= len(args) || args[i+1] != strconv.Itoa(uid) {
		t.Fatalf("darwinMountMsdosArgs() = %v; -u must be followed by uid %d", args, uid)
	}
	if i := slices.Index(args, "-g"); i < 0 || i+1 >= len(args) || args[i+1] != strconv.Itoa(gid) {
		t.Fatalf("darwinMountMsdosArgs() = %v; -g must be followed by gid %d", args, gid)
	}

	if !slices.Contains(args, "/dev/disk4s2") {
		t.Fatalf("darwinMountMsdosArgs() = %v; missing partition device", args)
	}
	if !slices.Contains(args, "/tmp/wendyos-config-xyz") {
		t.Fatalf("darwinMountMsdosArgs() = %v; missing mount point", args)
	}
}

// The args are handed to a privileged `sudo mount_msdos`. Reject anything that
// is not a real partition node or absolute mount point, matching the defensive
// validation darwinDDArgs applies to the raw disk path.
func TestDarwinMountMsdosArgsRejectsInvalidInput(t *testing.T) {
	for _, partDev := range []string{
		"",
		"/dev/disk4",    // whole disk, not a partition slice
		"/dev/rdisk4s2", // raw char device is not used for mounting
		"/dev/disk4s2 seek=0",
		"/dev/disk4s2;rm -rf /",
		"../../etc/passwd",
		"/tmp/wendyos.img",
	} {
		t.Run("partDev "+partDev, func(t *testing.T) {
			if _, err := darwinMountMsdosArgs(501, 20, partDev, "/tmp/m"); err == nil {
				t.Fatalf("darwinMountMsdosArgs(%q) error = nil; want validation error", partDev)
			}
		})
	}

	if _, err := darwinMountMsdosArgs(501, 20, "/dev/disk4s2", "relative/path"); err == nil {
		t.Fatalf("darwinMountMsdosArgs(relative mountPoint) error = nil; want validation error")
	}
	if _, err := darwinMountMsdosArgs(-1, 20, "/dev/disk4s2", "/tmp/m"); err == nil {
		t.Fatalf("darwinMountMsdosArgs(negative uid) error = nil; want validation error")
	}
}
