package commands

// This file is intentionally untagged (no _darwin suffix) so the pure parsing
// below is unit-testable on any host, mirroring disklister_dd_args.go. The
// darwin-only orchestration that consumes it lives in os_provision_darwin.go.

import "strings"

// parseMountPoint extracts the mount point for partDev from `mount` output,
// or "" when the device is not mounted.
//
// Lines look like:
//
//	/dev/disk4s2 on /Volumes/config (msdos, local, nodev, nosuid, noowners, fskit)
//
// Split on " on " and the last " (" so mount points containing spaces survive.
func parseMountPoint(mountOutput, partDev string) string {
	prefix := partDev + " on "
	for _, line := range strings.Split(mountOutput, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := line[len(prefix):]
		idx := strings.LastIndex(rest, " (")
		if idx <= 0 {
			continue
		}
		return rest[:idx]
	}
	return ""
}
