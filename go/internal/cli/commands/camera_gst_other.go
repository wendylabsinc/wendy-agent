//go:build !windows

package commands

import "path/filepath"

// gstLaunchName is the executable name searched on PATH.
const gstLaunchName = "gst-launch-1.0"

// gstUnixFallbackDirs are bin directories searched when gst-launch-1.0 is not
// on PATH. Includes Homebrew's Apple Silicon prefix (/opt/homebrew/bin), since
// brew on M-series Macs does not symlink into /usr/local. Declared as a var so
// tests can override it.
var gstUnixFallbackDirs = []string{
	"/usr/bin",
	"/usr/local/bin",
	"/usr/sbin",
	"/opt/homebrew/bin",
}

// gstLaunchFallbackPaths returns full candidate paths to gst-launch-1.0.
func gstLaunchFallbackPaths() []string {
	paths := make([]string, 0, len(gstUnixFallbackDirs))
	for _, dir := range gstUnixFallbackDirs {
		paths = append(paths, filepath.Join(dir, gstLaunchName))
	}
	return paths
}
