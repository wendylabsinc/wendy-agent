// Package flasher performs T264 (Thor) stage-2 flashing once stage-1 RCM boot has
// brought the device up as the initrd-flash ADB gadget.
//
// Rather than reimplementing NVIDIA's device-side flasher, it drives NVIDIA's own
// bootburn FlashImages() over ADB, unmodified, via a small monkeypatch driver
// (stage2_flash.py) that skips bootburn's i386-only boot/probe steps (our Go
// stage-1 already did the equivalent). bootburn's host side only invokes `adb`, so
// thor-flash points it at our self-contained wendy-adb shim by prepending --adb-dir
// to PATH — no Google adb binary, no adb server.
//
// Requirements at run time:
//   - python3 on PATH.
//   - The bundle dir is an extracted+generated WendyOS Thor tegraflash bundle: it
//     must contain unified_flash/tools/flashtools/bootburn (NVIDIA's scripts) and
//     out/flash_workspace (the generated flash images), produced on the Linux
//     builder. See package bringup for how the bundle is generated.
//   - The device must already be the ADB gadget with a working shell (the flashing
//     kernel booted) — see the bringup package and project notes on the rcm-flash
//     environment.
package flasher

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed stage2_flash.py
var stage2Driver []byte

// Options controls stage-2 flashing.
type Options struct {
	// BundleDir is the extracted+generated bundle root (contains unified_flash/ and
	// out/flash_workspace).
	BundleDir string
	// ADBDir, if set, is prepended to PATH so bootburn's `adb` calls resolve to our
	// wendy-adb shim (built as `adb`).
	ADBDir string
	// Board is the bootburn board name (default "jetson-t264").
	Board string
	Out    io.Writer
}

// Run drives bootburn's FlashImages over ADB via the monkeypatch driver.
func Run(opts Options) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	board := opts.Board
	if board == "" {
		board = "jetson-t264"
	}

	bootburnDir := filepath.Join(opts.BundleDir, "unified_flash", "tools", "flashtools", "bootburn")
	flashWorkspace := filepath.Join(opts.BundleDir, "out", "flash_workspace")
	if _, err := os.Stat(bootburnDir); err != nil {
		return fmt.Errorf("bootburn scripts not found at %s (need an extracted+generated bundle): %w", bootburnDir, err)
	}
	if _, err := os.Stat(flashWorkspace); err != nil {
		return fmt.Errorf("flash workspace not found at %s (run the Linux generate step first): %w", flashWorkspace, err)
	}

	python, err := exec.LookPath("python3")
	if err != nil {
		return fmt.Errorf("python3 not found on PATH: %w", err)
	}

	// Write the monkeypatch driver to a temp file; it adds the bootburn dirs to
	// sys.path itself (it runs with cwd = bootburnDir).
	driver, err := os.CreateTemp("", "wendy-stage2-*.py")
	if err != nil {
		return fmt.Errorf("creating driver temp file: %w", err)
	}
	defer os.Remove(driver.Name())
	if _, err := driver.Write(stage2Driver); err != nil {
		driver.Close()
		return fmt.Errorf("writing driver: %w", err)
	}
	driver.Close()

	// bootburn flash args (mirrors out/doflash.sh, minus the RCM --usb-instance).
	args := []string{driver.Name(), "-b", board, "--l4t", "-P", flashWorkspace}
	cmd := exec.Command(python, args...)
	cmd.Dir = bootburnDir
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = envWithADB(opts.ADBDir)

	if opts.ADBDir != "" {
		fmt.Fprintf(out, "Using adb from: %s\n", opts.ADBDir)
	}
	fmt.Fprintf(out, "Running bootburn flash: %s %v (cwd %s)\n", python, args, bootburnDir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bootburn flash failed: %w", err)
	}
	return nil
}

// envWithADB returns the environment with adbDir prepended to PATH (if set).
func envWithADB(adbDir string) []string {
	env := os.Environ()
	if adbDir == "" {
		return env
	}
	abs, err := filepath.Abs(adbDir)
	if err != nil {
		abs = adbDir
	}
	for i, kv := range env {
		if len(kv) >= 5 && kv[:5] == "PATH=" {
			env[i] = "PATH=" + abs + string(os.PathListSeparator) + kv[5:]
			return env
		}
	}
	return append(env, "PATH="+abs)
}
