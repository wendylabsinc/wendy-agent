package t234

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed prep.sh
var prepScript []byte

// prepImage is the container image the prep script runs in. The script
// apt-installs its tool set on top, so only base utilities matter here.
const prepImage = "ubuntu:24.04"

// prepDeps are the packages prep.sh needs (see its header).
const prepDeps = "python3 python3-yaml cpp device-tree-compiler openssl e2fsprogs util-linux"

// prepOKMarker is the last line prep.sh prints on success; its presence in a
// cached prep dir marks the prep as complete (a torn prep re-runs).
const prepOKMarker = "WENDY-T234-PREP-OK"

// ErrDockerMissing reports that no usable Docker daemon was found. The T234
// prep step needs one (it runs NVIDIA's x86-64 signing tools in a linux/amd64
// container); the caller turns this into user guidance.
var ErrDockerMissing = fmt.Errorf("docker is required to prepare a Jetson Orin flash (it runs NVIDIA's x86-64 signing tools in a container) — install/start Docker and re-run")

// Prepped reports whether bundleDir already holds a complete prep result.
func Prepped(bundleDir string) bool {
	data, err := os.ReadFile(filepath.Join(bundleDir, PrepDirName, ".complete"))
	return err == nil && strings.TrimSpace(string(data)) == prepOKMarker
}

// Prep runs the containerized prep step against an extracted bundle,
// producing wendy-prep/ (plan.json, flashpkg.ext4) and rcmboot_blob/ in
// bundleDir. It is idempotent and caches: a bundle already prepped returns
// immediately with cached=true. Verbose output goes to out.
func Prep(bundleDir string, out io.Writer) (cached bool, err error) {
	if Prepped(bundleDir) {
		return true, nil
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return false, ErrDockerMissing
	}
	if err := exec.Command(docker, "version", "--format", "{{.Server.Os}}").Run(); err != nil {
		return false, ErrDockerMissing
	}

	if err := os.WriteFile(filepath.Join(bundleDir, ".wendy-prep.sh"), prepScript, 0o755); err != nil {
		return false, fmt.Errorf("staging prep script: %w", err)
	}
	defer os.Remove(filepath.Join(bundleDir, ".wendy-prep.sh"))

	// --platform linux/amd64: the bundle's tegra tools are x86-64 ELF
	// binaries (Rosetta/qemu emulation runs them on arm64 hosts). apt output
	// is suppressed; the tegraflash output streams to the caller's writer.
	script := fmt.Sprintf(
		"apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq %s >/dev/null 2>&1 && exec ./.wendy-prep.sh",
		prepDeps)
	cmd := exec.Command(docker, "run", "--rm", "--platform", "linux/amd64",
		"-v", bundleDir+":/work", "-w", "/work", prepImage, "bash", "-c", script)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("flash prep container failed: %w", err)
	}

	// Validate before stamping so a torn prep re-runs next time.
	if _, err := LoadPlan(bundleDir); err != nil {
		return false, fmt.Errorf("prep completed but produced an unusable plan: %w", err)
	}
	if st, err := os.Stat(FlashpkgPath(bundleDir)); err != nil || st.Size() != flashpkgSize {
		return false, fmt.Errorf("prep completed but flashpkg.ext4 is missing or has the wrong size")
	}
	if _, err := ParseRCMBootCmd(bundleDir); err != nil {
		return false, fmt.Errorf("prep completed but the RCM boot blob is unusable: %w", err)
	}
	stamp := filepath.Join(bundleDir, PrepDirName, ".complete")
	if err := os.WriteFile(stamp, []byte(prepOKMarker+"\n"), 0o644); err != nil {
		return false, fmt.Errorf("stamping prep result: %w", err)
	}
	return false, nil
}

// flashpkgSize is the exact size of flashpkg.ext4: the device exports a
// 128 MiB LUN backing file (init-flash.sh dd bs=1M count=128) and the host
// replaces it wholesale.
const flashpkgSize = 128 << 20
