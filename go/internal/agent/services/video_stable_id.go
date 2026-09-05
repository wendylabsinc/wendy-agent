package services

// Stable identity for local V4L2 cameras.
//
// A camera's /dev/videoN number is assigned in enumeration order at boot, so it
// belongs to the boot rather than to the camera: a reboot or a replug can
// renumber the nodes. A caller that pinned a number then addresses whatever the
// kernel put there instead -- and because opening the wrong camera SUCCEEDS, it
// streams a valid picture from the wrong sensor and reports no error at all.
// `name` and `driver` cannot rescue that: they identify the model, so two
// cameras of the same type are indistinguishable.
//
// udev publishes two stable names per node, and they are not equally durable:
//
//	/dev/v4l/by-id/    vendor + product + serial   survives a reboot AND a move
//	                                               to another port
//	/dev/v4l/by-path/  the USB port topology       survives a reboot but NOT a
//	                                               move
//
// A camera gets ONE identity, not two: by-id where udev has one, by-path only
// where it does not. That case is real rather than theoretical -- two
// same-model cameras sharing a factory serial produce the same by-id name, so
// only one link exists and the camera that loses it has nothing else that tells
// it apart from its twin.
//
// The value carries the scheme it came from ("by-id:" / "by-path:") so a
// caller can see which of the two durability guarantees it actually holds,
// rather than having to know how udev names things.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	v4lByIDDir   = "/dev/v4l/by-id"
	v4lByPathDir = "/dev/v4l/by-path"

	// Scheme tags on the reported identity, so a caller can see which
	// durability guarantee it holds without knowing how udev names things.
	schemeByID   = "by-id"
	schemeByPath = "by-path"
)

// stableNames holds the udev names for one /dev/videoN node. Either may be
// empty: CSI and network cameras have no /dev/v4l entry, and a kernel without
// the udev rules has neither directory.
type stableNames struct {
	byID   string
	byPath string
}

// stableID is the single identity reported for a node: the by-id name when
// there is one, the port otherwise, empty when udev knows neither.
func (n stableNames) stableID() string {
	switch {
	case n.byID != "":
		return schemeByID + ":" + n.byID
	case n.byPath != "":
		return schemeByPath + ":" + n.byPath
	default:
		return ""
	}
}

// readStableNames maps a resolved device path ("/dev/video2") to its udev
// names.
//
// A missing directory is NOT an error. Plenty of devices have no /dev/v4l at
// all, and losing the enrichment is strictly better than failing enumeration --
// the numbers still work, they are simply not stable.
func readStableNames(byIDDir, byPathDir string) map[string]stableNames {
	out := map[string]stableNames{}
	for dev, name := range firstNamePerDevice(byIDDir) {
		n := out[dev]
		n.byID = name
		out[dev] = n
	}
	for dev, name := range firstNamePerDevice(byPathDir) {
		n := out[dev]
		n.byPath = name
		out[dev] = n
	}
	return out
}

// firstNamePerDevice resolves every symlink in dir and records its basename
// against the device node it points at.
//
// Entries are skipped rather than fatal: one dangling symlink must not cost
// every other camera its identity. os.ReadDir returns sorted entries, so where
// two names somehow reach the same node the winner is deterministic.
func firstNamePerDevice(dir string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		target, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if !strings.HasPrefix(filepath.Base(target), "video") {
			continue
		}
		if _, seen := out[target]; seen {
			continue
		}
		out[target] = e.Name()
	}
	return out
}

// resolveStableID returns the /dev/videoN number for a scheme-tagged stable id,
// or false.
//
// A "by-path:" name resolves even for a camera whose reported identity is its
// by-id: asking to stream from a physical port is legitimate, it just has to be
// asked for. Inheriting it from a listing is what we avoid.
//
// The match is exact. A substring or prefix rule would be friendlier to type
// and is precisely the wrong trade here: "usb-Acme_Camera_SN1" would match both
// "...SN1-video-index0" and "...SN10-video-index0", and the caller would get
// one of them with no indication that the name was ambiguous. An exact name is
// something ListVideoDevices already hands you.
func resolveStableID(names map[string]stableNames, stableID string) (uint32, bool) {
	scheme, want, tagged := strings.Cut(strings.TrimSpace(stableID), ":")
	if !tagged || want == "" {
		return 0, false
	}
	// An untagged or unknown scheme is not an identity this API ever handed
	// out. Guessing which of the two was meant is how a name for one camera
	// quietly resolves to another.
	var pick func(stableNames) string
	switch scheme {
	case schemeByID:
		pick = func(n stableNames) string { return n.byID }
	case schemeByPath:
		pick = func(n stableNames) string { return n.byPath }
	default:
		return 0, false
	}
	for devPath, n := range names {
		if pick(n) != want {
			continue
		}
		num, err := deviceNumber(devPath)
		if err != nil {
			return 0, false
		}
		return num, true
	}
	return 0, false
}

// deviceNumber extracts N from a "/dev/videoN" path. Same parse listCameras
// does on the glob results, kept here so the two cannot drift.
func deviceNumber(devPath string) (uint32, error) {
	base := filepath.Base(devPath)
	n, err := strconv.ParseUint(strings.TrimPrefix(base, "video"), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("not a video device path: %q", devPath)
	}
	return uint32(n), nil
}
