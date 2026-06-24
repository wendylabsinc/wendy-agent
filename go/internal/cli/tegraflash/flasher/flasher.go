// Package flasher performs T264 (Thor) stage-2 flashing once stage-1 RCM boot has
// brought the device up as the initrd-flash ADB gadget.
//
// Rather than reimplementing NVIDIA's device-side flasher, it drives NVIDIA's own
// bootburn FlashImages() over ADB, unmodified, via a small monkeypatch driver
// (stage2_flash.py) that skips bootburn's i386-only boot/probe steps (our Go
// stage-1 already did the equivalent). The host side is satisfied by our
// self-contained wendy-shim (built from cmd/wendy-shim, deployed as adb/lsusb/
// timeout) — no Google adb binary, no adb server.
//
// bootburn reaches those host tools two ways: it calls `lsusb`/`timeout` by bare
// name (PATH), and `adb` by an absolute path (<flash>/adb). So the operator both
// prepends --adb-dir (containing adb, lsusb, timeout) to PATH AND replaces the
// bundle's and workspace's tools/flashtools/flash/adb (the Linux x86 binary) with
// the shim.
//
// Requirements at run time:
//   - python3 on PATH (with PyYAML; e.g. a venv whose bin is on PATH).
//   - WorkspaceDir is a flash_workspace produced on a Linux x86_64 builder (the
//     "linux-stage2" artifact). It must contain flash-images/ with FileToFlash.txt
//     and all signed partition images. BootburnDir provides NVIDIA's bootburn
//     scripts (unified_flash/tools/flashtools/bootburn) from a local copy of the
//     same bundle.
//   - The device must already be the initrd-flash ADB gadget (the flashing kernel
//     booted) — see the bringup package. The flashing adbd advertises shell_v2; the
//     adb package uses that service, which is required for shell commands to run.
//
// Generating the linux-stage2 workspace (Linux x86_64 builder; NVIDIA's flash tools
// are x86_64 and do not run on macOS arm64).
//
// The whole normal flash is one script in the bundle, ./initrd-flash, which for
// T264 prepares images, sets up a unified flash workspace, RCM-boots the device,
// and flashes QSPI+NVMe over ADB. We only need its offline image-assembly steps
// (no device attached); the RCM boot and ADB flash are what thor-flash does from
// the Mac instead. From the extracted bundle root (where initrd-flash,
// .env.initrd-flash and boardvars.sh live):
//
//	. ./.env.initrd-flash ; . ./boardvars.sh
//	keyfile= ; sbk_keyfile= ; datafile="$DATAFILE"
//	serial_number= ; instance_args= ; PRESIGNED= ; partition_name=
//
//	# Reuse initrd-flash's own functions (definitions only; its main flow is what
//	# waits on USB), then run the headless subset:
//	eval "$(awk '/^prepare_binaries_t264\(\)/{f=1} f{print} f&&/^}/{exit}' initrd-flash)"
//	eval "$(awk '/^stage_files_for_uniflash\(\)/{f=1} f{print} f&&/^}/{exit}' initrd-flash)"
//	eval "$(awk '/^update_flash_cfg_for_partition\(\)/{f=1} f{print} f&&/^}/{exit}' initrd-flash)"
//
//	# 1. Prepare signed partition images (each runs tegra264-flash-helper.sh
//	#    --no-flash --sign ...; no device touched):
//	prepare_binaries_t264 internal flash.xml.in          "$LNXFILE"       "$ROOTFS_IMAGE" "$datafile"
//	prepare_binaries_t264 external external-flash.xml.in "$LNXFILE"       "$ROOTFS_IMAGE" "$datafile"
//	prepare_binaries_t264 rcm-boot rcmboot-flash.xml.in  initrd-flash.img "$ROOTFS_IMAGE" "$datafile"
//
//	# 2. Assemble the unified flash workspace:
//	convargs="--profile base --external-device $ROOTFS_DEVICE external-secureflash.xml"
//	./unified_flash/tools/flashtools/bootburn/create_bsp_images.py -b jetson-t264 --toolsonly -l -g "$PWD/out" --l4t
//	mkdir -p out/flash_workspace/flash-images out/flash_workspace/rcm-boot
//	./create_l4t_bsp_images.py $convargs --info --dest "$PWD/out"
//	./create_l4t_bsp_images.py $convargs --dest "$PWD/out/flash_workspace/flash-images"
//	./create_l4t_bsp_images.py $convargs --dest "$PWD/out/flash_workspace/rcm-boot" --rcm-boot
//	cp -R out/flash_workspace/rcm-boot out/flash_workspace/rcm-flash
//
// Ship the whole resulting out/ directory (flash_workspace/ + tools/) to the Mac as
// the linux-stage2 artifact; bootburn reads tools/ as flash_workspace/../tools, so
// the sibling layout must be preserved. Also produced under the bundle root is
// rcmboot_blob/ (the prepare rcm-boot output) — that is the linux-stage1 artifact
// the bringup package consumes (see that package). Example packaging:
//
//	tar czf linux-stage2.tar.gz -C out .          # flash_workspace/ + tools/
//	tar czf linux-stage1.tar.gz -C rcmboot_blob . # blob.bin, bcts, mb1, membct_*
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
	// BundleDir is a local copy of the extracted tegraflash bundle; it provides
	// NVIDIA's bootburn scripts (unified_flash/tools/flashtools/bootburn).
	BundleDir string
	// WorkspaceDir is the generated "out" directory from the Linux builder (the
	// "linux-stage2" artifact). It contains flash_workspace/ (with flash-images/
	// FileToFlash.txt + the signed partition images) and its sibling tools/ (which
	// bootburn reads as <flash_workspace>/../tools, e.g. for ToolsVersion.txt).
	// bootburn is invoked with -P <WorkspaceDir>/flash_workspace.
	WorkspaceDir string
	// ADBDir, if set, is prepended to PATH so bootburn's bare-name tool calls
	// (lsusb, timeout) resolve to wendy-shim. It should contain wendy-shim deployed
	// as adb, lsusb and timeout. (bootburn calls adb itself by absolute path, so the
	// bundle's/workspace's flash/adb must also be replaced with the shim.)
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

	// bootburn flash args (mirrors out/doflash.sh, minus the RCM --usb-instance).
	// -y disables pipelining so partitions flash serially: our wendy-shim adb claims
	// the USB interface exclusively, so the parallel path's concurrent adb processes
	// would collide ("failed to claim interface ... bad access").
	args := []string{driver.Name(), "-b", board, "--l4t", "-y", "-P", flashWorkspace}
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
