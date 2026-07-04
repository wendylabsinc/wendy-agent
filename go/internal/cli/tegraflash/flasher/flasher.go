// Package flasher performs T264 (Thor) stage-2 flashing once stage-1 RCM boot has
// brought the device up as the initrd-flash ADB gadget.
//
// Rather than reimplementing NVIDIA's device-side flasher, it drives NVIDIA's own
// bootburn FlashImages() over ADB, unmodified, via a small monkeypatch driver
// (stage2_flash.py) that skips bootburn's i386-only boot/probe steps (the Go stage-1
// already did the equivalent). bootburn shells out to adb/lsusb/timeout; wendy
// supplies those itself — it re-execs as those names (see package shim), with no
// Google adb binary and no adb server. The caller (commands.installThor) materializes
// a shim directory on PATH and replaces the bundle's flash/adb with the same shim;
// this package just sets up the environment and runs bootburn.
//
// Inputs come from a flashpack (see package flashpack), produced offline on an
// x86_64 builder by scripts/make-thor-flashpack.sh; the relevant dirs are the
// Options fields below. Run-time requirements: python3 on PATH (PyYAML ships in the
// flashpack and is put on PYTHONPATH here), and a device already up as the
// initrd-flash ADB gadget (its adbd advertises shell_v2, which the adb package uses).
package flasher

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// estFlashDuration is a rough estimate shown to the user. Measured ~15.4 min on a
// jetson-agx-thor devkit (NVMe) over USB; the rootfs A/B writes dominate.
const estFlashDuration = "around 15 minutes"

// ErrGadgetUnreachable is returned when the flash fails before a single byte is
// written — i.e. bootburn never established ADB with the initrd-flash gadget. The
// device was not modified, so the caller can advise a plain retry rather than
// warning about a half-flashed (UEFI-only) device.
var ErrGadgetUnreachable = errors.New("flashing gadget never came up over USB (ADB)")

// EnvADBProgress names the file the adb shim writes its running push byte-count to
// (cumulative for the current push, reset per push). Run points the shim at a temp
// file via this env var and polls it to display transfer throughput.
const EnvADBProgress = "WENDY_ADB_PROGRESS"

//go:embed stage2_flash.py
var stage2Driver []byte

// Options controls stage-2 flashing.
type Options struct {
	// BundleDir is a local copy of the extracted tegraflash bundle; it provides
	// NVIDIA's bootburn scripts (unified_flash/tools/flashtools/bootburn).
	BundleDir string
	// WorkspaceDir is the builder's generated "out" dir. It holds flash_workspace/
	// (with flash-images/FileToFlash.txt + the signed partition images) and a sibling
	// tools/ that bootburn reads as <flash_workspace>/../tools; bootburn runs with
	// -P <WorkspaceDir>/flash_workspace.
	WorkspaceDir string
	// ADBDir, if set, is prepended to PATH so bootburn's bare-name lsusb/timeout calls
	// resolve to wendy's shim. (bootburn also calls adb by absolute path, so the
	// caller replaces the bundle's flash/adb with the shim too.)
	ADBDir string
	// ADBPort, if set, pins the flashing gadget to a specific USB location (a
	// PathKey) via WENDY_ADB_PATH, which the adb shim honors. Lets a multi-device
	// host flash the chosen board across the RCM→ADB re-enumeration.
	ADBPort string
	// LogPath is where bootburn's full output is written. If empty, a flash.log is
	// written alongside the workspace.
	LogPath string
	// PyYAMLDir is the flashpack's pure-python PyYAML dir (contains a yaml/ package);
	// it is prepended to PYTHONPATH so bootburn's `import yaml` resolves without pip
	// or a system PyYAML (macOS system python has none).
	PyYAMLDir string
	// Board is the bootburn board name (default "jetson-t264").
	Board string
	Out   io.Writer
	// Progress, if set, receives the running USB-push byte count (accumulated
	// across partitions) and the estimated total (summed from the flash plan's
	// image files; 0 when the plan couldn't be sized). Called ~once per second.
	// The count tracks transfers only — bootburn's signing/verification time is
	// invisible to it — and can slightly overshoot total on push retries.
	Progress func(written, total int64)
}

// Run drives bootburn's FlashImages over ADB via the monkeypatch driver.
// Cancelling ctx aborts the flash: the bootburn process group is killed and
// ctx's error is returned. The child runs in its own process group, so a
// terminal ctrl+c does not reach it — cancelling ctx is the only way to stop it.
func Run(ctx context.Context, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	board := opts.Board
	if board == "" {
		board = "jetson-t264"
	}

	bootburnDir := filepath.Join(opts.BundleDir, "unified_flash", "tools", "flashtools", "bootburn")
	flashWorkspace := filepath.Join(opts.WorkspaceDir, "flash_workspace")
	if _, err := os.Stat(bootburnDir); err != nil {
		return fmt.Errorf("bootburn scripts not found at %s (need a local copy of the extracted bundle): %w", bootburnDir, err)
	}
	if _, err := os.Stat(filepath.Join(flashWorkspace, "flash-images", "FileToFlash.txt")); err != nil {
		return fmt.Errorf("flash workspace at %s is missing flash-images/FileToFlash.txt (need the linux-stage2 'out' artifact): %w", flashWorkspace, err)
	}

	python, err := exec.LookPath("python3")
	if err != nil {
		return fmt.Errorf("python3 not found on PATH: %w", err)
	}

	// Write the monkeypatch driver to a temp file. The name must contain
	// "flash_bsp_images": bootburn special-cases that argv[0] to take the normal
	// flashing path (e.g. it then tolerates a chip version not read from the device,
	// which our skipped ECID step would have set). It adds the bootburn dirs to
	// sys.path itself (it runs with cwd = bootburnDir).
	driver, err := os.CreateTemp("", "flash_bsp_images-wendy-*.py")
	if err != nil {
		return fmt.Errorf("creating driver temp file: %w", err)
	}
	defer os.Remove(driver.Name())
	if _, err := driver.Write(stage2Driver); err != nil {
		driver.Close()
		return fmt.Errorf("writing driver: %w", err)
	}
	driver.Close()

	// PyYAML ships in the flashpack; it goes on PYTHONPATH (in envWithADB) so
	// bootburn's `import yaml` resolves without pip or a system PyYAML.
	pyDir := opts.PyYAMLDir
	if pyDir != "" {
		if _, err := os.Stat(filepath.Join(pyDir, "yaml", "__init__.py")); err != nil {
			return fmt.Errorf("PyYAML not found in flashpack at %s (need stage2/pyyaml/yaml): %w", pyDir, err)
		}
	}

	// Flash args mirror out/doflash.sh minus the RCM --usb-instance. -y disables
	// pipelining so partitions flash serially: our adb shim claims the USB interface
	// exclusively, so the parallel path's concurrent adb processes would collide.
	args := []string{driver.Name(), "-b", board, "--l4t", "-y", "-P", flashWorkspace}

	// bootburn is extremely verbose and emits nothing useful per-partition at the
	// normal level (it captures the adb/nvdd I/O internally), so stream its full
	// output to a log file and show a curated summary here instead.
	logPath := opts.LogPath
	if logPath == "" {
		logPath = filepath.Join(opts.WorkspaceDir, "flash.log")
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("creating flash log: %w", err)
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "$ %s %s\n(cwd %s)\n\n", python, strings.Join(args, " "), bootburnDir)

	// The adb shim writes its running push byte-count here so we can show live
	// throughput; best-effort, so a temp-file failure just disables the readout.
	progressPath := ""
	if pf, perr := os.CreateTemp("", "thor-flash-progress-*"); perr == nil {
		progressPath = pf.Name()
		pf.Close()
		defer os.Remove(progressPath)
	}

	cmd := exec.Command(python, args...)
	cmd.Dir = bootburnDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = envWithADB(opts.ADBDir, pyDir, opts.ADBPort, progressPath)
	setProcessGroup(cmd)

	// Up-front plan from FileToFlash.txt, so the (long, mostly silent) write reads
	// as deliberate progress rather than a hang.
	plan := summarizeFlashPlan(filepath.Join(flashWorkspace, "flash-images", "FileToFlash.txt"))
	fmt.Fprintf(out, "Writing %d partitions to QSPI + internal NVMe over USB.\n", plan.count)
	if plan.summary != "" {
		fmt.Fprintf(out, "  Includes: %s\n", plan.summary)
	}
	fmt.Fprintf(out, "  Expect %s; the rootfs (A/B slots) is the bulk and writes silently for several minutes — that is normal.\n", estFlashDuration)
	fmt.Fprintf(out, "  Full log: %s\n", logPath)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting bootburn flash: %w", err)
	}

	// Sample bytes-pushed every second (to tell a genuine failure from a gadget
	// that never came up) and log a heartbeat every minute so a multi-minute silent
	// rootfs write doesn't look hung in the verbose/CI log.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	start := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var maxBytes int64 // most bytes seen pushed; 0 means nothing was ever written
	// The shim's progress counter is per-push (reset for each partition), so the
	// running total accumulates completed pushes: a counter that shrank means a
	// new push started and the previous one's final count is banked.
	var pushAccum, lastPush int64
	lastHeartbeat := start
	for {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
			<-done // reap; ignore the kill-induced error
			fmt.Fprintf(out, "Flash aborted after %s.\n", elapsed(start))
			return ctx.Err()
		case werr := <-done:
			if n, ok := readProgressFile(progressPath); ok && n > maxBytes {
				maxBytes = n
			}
			if werr != nil {
				return flashFailure(out, logPath, elapsed(start).String(), maxBytes, werr)
			}
			fmt.Fprintf(out, "Partitions written in %s.\n", elapsed(start))
			return nil
		case now := <-tick.C:
			if n, ok := readProgressFile(progressPath); ok {
				if n > maxBytes {
					maxBytes = n
				}
				if n < lastPush {
					pushAccum += lastPush
				}
				lastPush = n
				if opts.Progress != nil {
					opts.Progress(pushAccum+n, plan.totalBytes)
				}
			}
			if now.Sub(lastHeartbeat) >= 60*time.Second {
				lastHeartbeat = now
				fmt.Fprintf(out, "  … still flashing (%s elapsed)\n", elapsed(start))
			}
		}
	}
}

// flashFailure writes a concise failure summary (the full bootburn output is in
// the log) and returns a classified error. When nothing was written it wraps
// ErrGadgetUnreachable so the caller can advise a plain retry.
func flashFailure(out io.Writer, logPath, took string, maxBytes int64, werr error) error {
	reason := classifyFlashFailure(logPath)
	fmt.Fprintf(out, "Flash failed after %s: %s\n", took, reason)
	fmt.Fprintf(out, "Full log: %s\n", logPath)
	if maxBytes == 0 {
		return fmt.Errorf("%w (full log: %s): %v", ErrGadgetUnreachable, logPath, werr)
	}
	return fmt.Errorf("bootburn flash failed after writing data (full log: %s): %w", logPath, werr)
}

// classifyFlashFailure turns the tail of the bootburn log into a one-line human
// reason. Best-effort: an unrecognized failure points the user at the log.
func classifyFlashFailure(logPath string) string {
	tail := tailFile(logPath, 60)
	switch {
	// Check access errors before the generic timeout: a denied gadget also times
	// out, and the wait-for-device retries log the access-denied reason. Match
	// only the exact markers our own code emits (adb.openOnce's message and
	// libusb's "bad access [code -3]" as relayed by the adb shim) so an image
	// filename or device string can't trip this branch.
	case strings.Contains(tail, "USB access denied opening the flashing gadget"),
		strings.Contains(tail, "bad access [code"):
		return "USB access denied opening the flashing gadget — on Linux install the wendy udev rule (USB vendor 0955) or run with sudo; on macOS quit whatever holds the gadget (e.g. `adb kill-server`)"
	case strings.Contains(tail, "ADB_TIMEOUT"), strings.Contains(tail, "adb wait-for-device"):
		return "the flashing gadget never came up over USB (ADB timeout)"
	case strings.Contains(tail, "No such file"), strings.Contains(tail, "not found"):
		return "a required flash file was missing (see log)"
	default:
		return "see the log for details"
	}
}

// readProgressFile reads the single integer byte-count the adb shim writes.
func readProgressFile(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// flashPlan is a human summary of FileToFlash.txt.
type flashPlan struct {
	count      int    // number of partition entries
	summary    string // notable partitions, e.g. "ESP, rootfs (A/B), config"
	totalBytes int64  // sum of the listed image files' sizes (0 if none could be sized)
}

// summarizeFlashPlan parses FileToFlash.txt for a count, the notable large
// NVMe partitions, and a total-bytes estimate (each entry's image file — column
// 3, living next to FileToFlash.txt — stat'ed and summed per occurrence, since
// A/B slots push the same image twice). Best-effort: a parse or stat miss just
// yields a thinner summary / smaller total.
func summarizeFlashPlan(fileToFlash string) flashPlan {
	var p flashPlan
	f, err := os.Open(fileToFlash)
	if err != nil {
		return p
	}
	defer f.Close()
	imagesDir := filepath.Dir(fileToFlash)
	var hasESP, hasRootfs, hasConfig bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p.count++
		if fields := strings.Fields(line); len(fields) >= 3 {
			// Base() confines the stat to imagesDir; plan entries are bare
			// filenames, so anything with path components is malformed anyway.
			if name := filepath.Base(fields[2]); name != "." && name != ".." && name != "/" {
				if fi, serr := os.Stat(filepath.Join(imagesDir, name)); serr == nil {
					p.totalBytes += fi.Size()
				}
			}
		}
		low := strings.ToLower(line)
		switch {
		case strings.Contains(low, "esp.img"):
			hasESP = true
		case strings.Contains(low, ".simg"):
			hasRootfs = true
		case strings.Contains(low, "config-partition"):
			hasConfig = true
		}
	}
	var parts []string
	if hasESP {
		parts = append(parts, "ESP")
	}
	if hasRootfs {
		parts = append(parts, "rootfs (A/B)")
	}
	if hasConfig {
		parts = append(parts, "config")
	}
	p.summary = strings.Join(parts, ", ")
	return p
}

// elapsed formats time since start to whole seconds.
func elapsed(start time.Time) time.Duration { return time.Since(start).Round(time.Second) }

// tailFile returns the last n lines of a file (best-effort, for error context).
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// envWithADB returns the environment with adbDir prepended to PATH, pyDir prepended
// to PYTHONPATH, WENDY_ADB_PATH set to adbPort, and EnvADBProgress set to
// progressPath (each only when non-empty). The adb shim inherits WENDY_ADB_PATH to
// target the selected device and EnvADBProgress to report transfer progress.
func envWithADB(adbDir, pyDir, adbPort, progressPath string) []string {
	env := os.Environ()
	if adbDir != "" {
		if abs, err := filepath.Abs(adbDir); err == nil {
			adbDir = abs
		}
		env = prependEnv(env, "PATH", adbDir)
	}
	if pyDir != "" {
		env = prependEnv(env, "PYTHONPATH", pyDir)
	}
	if adbPort != "" {
		env = append(env, "WENDY_ADB_PATH="+adbPort)
	}
	if progressPath != "" {
		env = append(env, EnvADBProgress+"="+progressPath)
	}
	return env
}

// prependEnv prepends val to a path-list env var (key), preserving any existing value.
func prependEnv(env []string, key, val string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			old := kv[len(prefix):]
			if old == "" {
				env[i] = prefix + val
			} else {
				env[i] = prefix + val + string(os.PathListSeparator) + old
			}
			return env
		}
	}
	return append(env, prefix+val)
}
