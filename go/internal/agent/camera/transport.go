// Package camera provides shared helpers for classifying physical camera
// transport (CSI vs USB vs Unknown) and enumerating libcamera-visible cameras.
//
// The classifier is sysfs-driven: it inspects /sys/class/video4linux/<base>
// to find the kernel driver bound to a /dev/videoN node and maps it to a
// transport. The libcamera enumerator shells out to the `cam` tool from
// libcamera-tools and is best-effort — if `cam` is missing the enumerator
// returns (nil, nil) so callers degrade gracefully.
//
// Lives in its own package so both internal/agent/services and
// internal/agent/hardware can import it without creating a cycle.
package camera

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Transport identifies how a video device is physically attached.
type Transport int

const (
	TransportUnknown Transport = iota
	TransportUSB
	TransportCSI
)

// String returns a lowercase, human-readable transport label.
func (t Transport) String() string {
	switch t {
	case TransportUSB:
		return "usb"
	case TransportCSI:
		return "csi"
	default:
		return "unknown"
	}
}

// Injection points for tests. Real production uses os.Readlink/os.Stat and
// the standard PATH lookup.
var (
	sysfsRoot         = "/sys/class/video4linux"
	readDriverSymlink = func(path string) (string, error) { return os.Readlink(path) }
	statPath          = func(path string) error { _, err := os.Stat(path); return err }
	lookupCam         = func() (string, error) {
		if p, err := exec.LookPath("cam"); err == nil {
			return p, nil
		}
		for _, dir := range []string{"/usr/bin", "/usr/local/bin"} {
			candidate := filepath.Join(dir, "cam")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		return "", errors.New("cam binary not found")
	}
)

// usbDriverPrefixes are kernel driver names that bind USB video devices.
var usbDriverPrefixes = []string{
	"uvcvideo",
	"usbtv",
	"gspca_",
}

// csiDriverPrefixes are kernel driver names that bind CSI/MIPI capture
// devices and on-board sensor subdevs. Matching is case-insensitive and uses
// prefix semantics, so sensor drivers like "imx477", "imx219", "ov5647" are
// all caught by the "imx" / "ov" entries.
var csiDriverPrefixes = []string{
	"tegra-capture-vi",
	"tegra-camera",
	"tegra-video",
	"nvcsi",
	"unicam",
	"bcm2835-unicam",
	"bcm2835-isp",
	"rkisp1",
	"rzg2l-cru",
	"imx",
	"ov",
}

// Classify returns the transport for /dev/<base> (e.g. base == "video0").
// driver is the kernel driver name read from sysfs, or "" when unavailable.
func Classify(base string) (Transport, string) {
	driverLink := filepath.Join(sysfsRoot, base, "device", "driver")
	target, err := readDriverSymlink(driverLink)
	driver := ""
	if err == nil {
		driver = filepath.Base(target)
	}
	if t := transportFromDriver(driver); t != TransportUnknown {
		return t, driver
	}
	// Presence of a device-tree node strongly implies a non-USB on-board
	// capture device — treat as CSI even when the driver name is unrecognized.
	if statPath(filepath.Join(sysfsRoot, base, "device", "of_node")) == nil {
		return TransportCSI, driver
	}
	return TransportUnknown, driver
}

func transportFromDriver(driver string) Transport {
	if driver == "" {
		return TransportUnknown
	}
	lower := strings.ToLower(driver)
	for _, p := range usbDriverPrefixes {
		if strings.HasPrefix(lower, p) {
			return TransportUSB
		}
	}
	for _, p := range csiDriverPrefixes {
		if strings.HasPrefix(lower, p) {
			return TransportCSI
		}
	}
	return TransportUnknown
}

// maxCamListBytes caps how much `cam --list` output is read into memory. Real
// output is a few short lines; the bound prevents a pathological or compromised
// `cam` binary from exhausting agent memory — libcameraTimeout bounds how long
// the subprocess runs, but not how much it can write.
const maxCamListBytes = 1 << 20 // 1 MiB

// readCamListBounded reads at most maxCamListBytes from r. Output beyond the cap
// is silently truncated; cam --list never approaches the bound in practice.
func readCamListBounded(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxCamListBytes))
}

// runCamList is the injection point for executing `cam --list`. Tests
// override this to return canned output without spawning a subprocess.
var runCamList = func(ctx context.Context, binary string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, "--list")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := readCamListBounded(stdout)
	// Drain anything past the cap so the child never blocks writing to a full
	// pipe, then reap it (also surfaces the context-timeout kill as a wait error).
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return data, nil
}

// EnumerateLibcamera invokes `cam --list` with a 1-second timeout and parses
// the output. The returned map is keyed by libcamera camera-name (the value
// suitable for `libcamerasrc camera-name=...`) and the value is the
// human-readable hint that appeared before the parenthesized ID.
//
// Returns (nil, nil) if the `cam` binary is not installed — callers should
// treat this as "no enrichment available" and not as an error.
func EnumerateLibcamera(ctx context.Context) (map[string]string, error) {
	bin, err := lookupCam()
	if err != nil {
		return nil, nil
	}
	subCtx, cancel := context.WithTimeout(ctx, libcameraTimeout)
	defer cancel()
	out, err := runCamList(subCtx, bin)
	if err != nil {
		return nil, err
	}
	return parseCamList(string(out)), nil
}

// libcameraTimeout caps how long `cam --list` is allowed to run. Exposed as a
// var so tests can shorten it.
var libcameraTimeout = time.Second

// IsValidLibcameraID reports whether id is safe to interpolate into a
// GStreamer pipeline as `libcamerasrc camera-name=<id>`. A libcamera ID is a
// device-tree-style path (e.g. "/base/soc/i2c0mux/i2c@1/imx477@1a"), so the
// allowlist is restricted to path, alphanumeric and a small set of punctuation
// characters. Anything containing whitespace or GStreamer control characters
// (notably ' ', '!', '=') is rejected, so a malformed or hostile ID can never
// inject additional pipeline elements once the pipeline string is split with
// strings.Fields downstream.
func IsValidLibcameraID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/' || r == '@' || r == ':' || r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// parseCamList extracts (id, hint) pairs from `cam --list` output.
// Sample line shape:
//
//	0: 'IMX477' (/base/soc/i2c0mux/i2c@1/imx477@1a)
//
// The parser keys only on the structural separators "'", ":" and "(", ")".
// It never matches on sensor model strings, so it works for any sensor.
//
// IDs that fail IsValidLibcameraID are dropped: a libcamera-name that cannot be
// safely interpolated into a GStreamer pipeline is useless to us (we'd fall back
// to auto-select anyway) and keeping it out of the map keeps every downstream
// consumer — `device camera list` surfacing and the capture pipeline — clean.
func parseCamList(out string) map[string]string {
	result := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		open := strings.LastIndex(line, "(")
		close := strings.LastIndex(line, ")")
		if open < 0 || close < 0 || close <= open+1 {
			continue
		}
		id := line[open+1 : close]
		if !IsValidLibcameraID(id) {
			continue
		}
		hint := strings.TrimSpace(line[:open])
		// Strip a leading "N:" index if present.
		if idx := strings.Index(hint, ":"); idx >= 0 {
			hint = strings.TrimSpace(hint[idx+1:])
		}
		hint = strings.Trim(hint, "'\"")
		result[id] = hint
	}
	return result
}
