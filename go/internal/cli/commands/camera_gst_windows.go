//go:build windows

package commands

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// gstLaunchName is the executable name searched on PATH. The .exe suffix is
// required when probing fallback paths directly via os.Stat (exec.LookPath
// applies PATHEXT itself, but os.Stat does not).
const gstLaunchName = "gst-launch-1.0.exe"

// gstRootEnvVars are the environment variables the machine-wide GStreamer MSI
// sets to point at its install root. The binaries live under "<root>\bin".
// Declared as a var so tests can override it.
var gstRootEnvVars = []string{
	"GSTREAMER_1_0_ROOT_MSVC_X86_64",
	"GSTREAMER_1_0_ROOT_MINGW_X86_64",
	"GSTREAMER_1_0_ROOT_X86_64",
	"GSTREAMER_1_0_ROOT_MSVC_X86",
	"GSTREAMER_1_0_ROOT_MINGW_X86",
}

// gstDefaultRoots are default install roots used as a last-resort backstop.
// Declared as a var so tests can override it.
var gstDefaultRoots = []string{
	`C:\gstreamer\1.0\msvc_x86_64`,
	`C:\gstreamer\1.0\mingw_x86_64`,
	`C:\gstreamer\1.0\x86_64`,
}

// gstRegistryRootsFn indirects gstRootsFromRegistry so tests can stub the
// registry lookup.
var gstRegistryRootsFn = gstRootsFromRegistry

// gstLaunchFallbackPaths returns full candidate paths to gst-launch-1.0.exe.
// Order of preference:
//  1. The InstallLocation recorded in the Windows uninstall registry. This is
//     authoritative and is the only thing that locates the winget "gstreamer"
//     package, which installs per-user under %LOCALAPPDATA%\Programs\gstreamer
//     without touching PATH or the GSTREAMER_1_0_ROOT_* variables.
//  2. The installer environment variables (set by machine-wide MSI installs).
//  3. Hardcoded default install roots, including the per-user winget layout.
func gstLaunchFallbackPaths() []string {
	var paths []string

	for _, root := range gstRegistryRootsFn() {
		paths = append(paths, filepath.Join(root, "bin", gstLaunchName))
	}

	for _, env := range gstRootEnvVars {
		if root := os.Getenv(env); root != "" {
			paths = append(paths, filepath.Join(root, "bin", gstLaunchName))
		}
	}

	roots := append([]string{}, gstDefaultRoots...)
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		roots = append(roots,
			filepath.Join(la, "Programs", "gstreamer", "1.0", "msvc_x86_64"),
			filepath.Join(la, "Programs", "gstreamer", "1.0", "mingw_x86_64"),
		)
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		roots = append(roots,
			filepath.Join(pf, "GStreamer", "1.0", "msvc_x86_64"),
			filepath.Join(pf, "GStreamer", "1.0", "mingw_x86_64"),
		)
	}
	for _, root := range roots {
		paths = append(paths, filepath.Join(root, "bin", gstLaunchName))
	}

	return paths
}

// sanitizeGSTRoot canonicalises a registry-supplied install root and rejects
// values that aren't absolute or that contain traversal segments. HKCU is
// user-writable, so a low-privileged process could create a fake "GStreamer"
// uninstall entry pointing at an attacker-controlled directory; this filter
// drops the obviously malformed cases. It is not a full trust boundary —
// callers must still treat the resulting binary as user-supplied.
func sanitizeGSTRoot(loc string) (string, bool) {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return "", false
	}
	// Reject obvious traversal in the raw input. filepath.Clean would collapse
	// these away, but the presence of ".." is a signal the registry entry is
	// malformed or hostile, so refuse it outright.
	for _, sep := range []string{`\`, `/`} {
		for _, part := range strings.Split(loc, sep) {
			if part == ".." {
				return "", false
			}
		}
	}
	cleaned := filepath.Clean(loc)
	if !filepath.IsAbs(cleaned) {
		return "", false
	}
	return cleaned, true
}

// gstRootsFromRegistry returns GStreamer install locations recorded under the
// standard Windows uninstall registry keys. Both machine (HKLM, including the
// 32-bit WOW6432Node view) and per-user (HKCU) hives are checked, since winget
// performs a per-user install.
func gstRootsFromRegistry() []string {
	type hive struct {
		root registry.Key
		path string
	}
	hives := []hive{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
	}

	var roots []string
	for _, h := range hives {
		base, err := registry.OpenKey(h.root, h.path, registry.READ)
		if err != nil {
			continue
		}
		names, err := base.ReadSubKeyNames(-1)
		base.Close()
		if err != nil {
			continue
		}
		for _, name := range names {
			sub, err := registry.OpenKey(h.root, h.path+`\`+name, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			displayName, _, _ := sub.GetStringValue("DisplayName")
			if strings.HasPrefix(displayName, "GStreamer") {
				if loc, _, err := sub.GetStringValue("InstallLocation"); err == nil {
					if cleaned, ok := sanitizeGSTRoot(loc); ok {
						roots = append(roots, cleaned)
					}
				}
			}
			sub.Close()
		}
	}
	return roots
}
