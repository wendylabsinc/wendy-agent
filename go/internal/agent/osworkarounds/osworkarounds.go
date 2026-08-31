// Package osworkarounds holds behaviour the agent must change to compensate for
// bugs in specific WendyOS releases it may be running on.
//
// The agent is updated independently of the OS and frequently runs on an older
// image than it was built alongside, so it sometimes has to work around a defect
// in the OS beneath it. Those compensations live here rather than inline at their
// call sites so they are easy to find, easy to audit, and — most importantly —
// easy to delete: each one records the release that fixed it and the condition
// under which it can be removed.
//
// One file per workaround. Add a field to Set, document what it compensates for,
// which releases are affected, and its removal condition.
package osworkarounds

import (
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

// Set is the set of workarounds that apply to a running OS version. The zero
// value means "no workarounds", which is what a current OS must always get.
type Set struct {
	// CleanRebootForCapsuleDurability makes the agent flush filesystems and
	// reboot via systemd instead of restarting the kernel immediately.
	//
	// Affects WendyOS < 0.18.1 (WDY-2200). Those images ship a wendyos-update
	// whose SwapSlot stages the UEFI capsule onto the vfat ESP and fsyncs only
	// the capsule file, never the filesystem. An immediate restart therefore
	// loses it, UEFI finds no capsule, and because the capsule branch
	// deliberately skips `nvbootctrl set-active-boot-slot`, the rootfs slot never
	// moves either — so every Jetson OTA installs, reboots onto the old slot and
	// rolls back. wendyos-update cb2c7b5 fixed its half with a Syncfs of the ESP,
	// but an OTA is performed by the updater on the *currently running* slot, so
	// an affected device cannot deliver its own fix. The agent can, because the
	// agent is what reboots.
	//
	// Remove when no supported upgrade path starts below 0.18.1.
	CleanRebootForCapsuleDurability bool
}

// For returns the workarounds that apply to osVersion.
//
// osVersion may carry the "WendyOS-" display prefix the agent reports. It fails
// open: an empty, dev, or unparseable version yields the zero Set, so a
// development or CI image never silently takes a different code path than the one
// it was tested with. This mirrors the CLI's requireReflashableOSVersion.
func For(osVersion string) Set {
	return Set{
		CleanRebootForCapsuleDurability: predatesRelease(osVersion, capsuleDurabilityFixedIn),
	}
}

// predatesRelease reports whether osVersion is a WendyOS version older than
// release, i.e. whether it is missing a fix that shipped in release.
//
// It fails open in every uncertain case — an empty version, a dev build, or a
// string that does not clearly sort below release — because a workaround applied
// to the wrong image is a silent behaviour change on an image nobody tested that
// way. version.CompareVersions splits on "." and "-", so "0.18.0-nightly" sorts
// below "0.18.1" (affected) while "0.18.1-nightly" sorts at or above it.
func predatesRelease(osVersion, release string) bool {
	normalized := strings.TrimPrefix(osVersion, "WendyOS-")
	if normalized == "" || version.IsDev(normalized) {
		return false
	}
	return version.CompareVersions(normalized, release) < 0
}
