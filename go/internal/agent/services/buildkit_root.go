package services

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
)

// defaultBuildkitRoot is buildkitd's own default for --root.
//
// On a general-purpose Linux box this is unremarkable. On an image-based OS it
// is the A/B root filesystem: measured on a Jetson AGX Thor running WendyOS,
// 12 GB with 4.6 GB free, beside a /data partition with 862 GB. A TensorRT build
// cache fills the partition the OS boots from, so getting this wrong damages a
// device rather than failing a build.
const defaultBuildkitRoot = "/var/lib/buildkit"

// buildkitConfigPath is where buildkitd looks for its config unless told
// otherwise, per `buildkitd --help`.
const buildkitConfigPath = "/etc/buildkit/buildkitd.toml"

// buildkitRoot reports where the RUNNING buildkitd keeps its state, preferring
// evidence over assumption:
//
//  1. the daemon's own command line — authoritative, because a flag beats any
//     file it might also have read;
//  2. the config file selected by the daemon's `--config`, or buildkitd's
//     default config path — what a daemon started without --root uses;
//  3. buildkitd's documented default.
//
// The order matters. Reading only the config would report a path the daemon is
// not using the moment someone passes --root, which is exactly what a WendyOS
// install has to do — and a wrong path here produces a confident, wrong
// free-space number, which is worse than no number at all.
//
// An empty return means "could not determine", and callers must not read that
// as safe.
func buildkitRoot(procDir string) string {
	root, configPath := pathsFromRunningDaemon(procDir)
	if root != "" {
		return root
	}
	if configPath != "" {
		root, err := rootFromConfig(configPath)
		if err != nil {
			// The running daemon may have loaded a config that has since been
			// changed or removed. Its effective root can no longer be inferred.
			return ""
		}
		if root != "" {
			return root
		}
		return defaultBuildkitRoot
	}

	root, err := rootFromConfig(buildkitConfigPath)
	if err == nil {
		if root != "" {
			return root
		}
		return defaultBuildkitRoot
	}
	if os.IsNotExist(err) {
		// No default config is a normal buildkitd installation: the daemon
		// uses its documented root in that case.
		return defaultBuildkitRoot
	}
	return ""
}

// pathsFromRunningDaemon scans /proc for a buildkitd process and returns the
// --root and --config paths it was started with, if any. A daemon running
// without --root returns an empty root so the caller can read the selected
// config file. Both flags accept their space-separated and equals forms.
//
// Known limit: with more than one buildkitd running, this reports the first one
// /proc yields, which is not necessarily the one holding the socket the agent
// builds through. Observed while testing, by starting a second daemon by hand.
// Not worth resolving by parsing --addr and matching the socket: two daemons on
// one host is a broken state either way, and a wrong-but-plausible root here is
// still better than the alternative of reporting nothing.
func pathsFromRunningDaemon(procDir string) (root, configPath string) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procDir, e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		// argv[0] only: matching anywhere would also hit the `buildctl` client
		// and any shell that merely mentions buildkitd.
		argv0 := string(raw)
		if i := strings.IndexByte(argv0, 0); i >= 0 {
			argv0 = argv0[:i]
		}
		if filepath.Base(argv0) != "buildkitd" {
			continue
		}
		root = procFlagValue(raw, "--root")
		configPath = procFlagValue(raw, "--config")
		return root, configPath
	}
	return "", ""
}

// procFlagValue reads an exact argv entry from a NUL-separated /proc cmdline.
// It accepts both forms supported by buildkitd while avoiding substring matches
// inside unrelated arguments.
func procFlagValue(raw []byte, name string) string {
	argv := strings.Split(string(raw), "\x00")
	for i := 1; i < len(argv); i++ {
		if argv[i] == name && i+1 < len(argv) {
			return strings.TrimSpace(argv[i+1])
		}
		if value, ok := strings.CutPrefix(argv[i], name+"="); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// rootFromConfig reads `root = "..."` out of buildkitd.toml.
//
// Decode only the top-level field so worker tables with their own `root` keys
// cannot be mistaken for the daemon's state directory. Errors are returned so
// the caller can distinguish an absent default config (which means buildkitd's
// default root) from a selected config whose effective root is now unknown.
func rootFromConfig(path string) (string, error) {
	var cfg struct {
		Root string `toml:"root"`
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return "", err
	}
	return cfg.Root, nil
}

// buildkitRootSpace reports total and available bytes for the filesystem
// holding path, walking up to the nearest existing ancestor.
//
// The walk matters: the state directory does not exist until the daemon's first
// build, and reporting zero for a path that is merely absent would look
// identical to a full disk.
//
// Named for its caller rather than "diskUsage", which is already a struct in
// this package describing used/total for the device-info path.
func buildkitRootSpace(path string) (total, free uint64) {
	if path == "" {
		return 0, 0
	}
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err == nil {
			bs := uint64(st.Bsize)
			// Available-to-unprivileged, not free: buildkitd runs as root here,
			// but reporting the larger number would overstate headroom on any
			// filesystem with reserved blocks.
			if bs == 0 {
				return 0, 0
			}
			return st.Blocks * bs, st.Bavail * bs
		}
		if p == "/" || p == "." {
			return 0, 0
		}
	}
}
