//go:build darwin || linux

package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/bringup"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/rcm"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/t234"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// Step IDs for the Orin flashing progress list.
const (
	orinStepDownload = iota
	orinStepPrep
	orinStepRCMBoot
	orinStepCommands
	orinStepWrite
	orinStepStatus
)

// installOrin flashes a Jetson AGX Orin's onboard eMMC (and QSPI boot flash)
// over USB recovery, from the meta-tegra tegraflash bundle the manifest
// publishes as the version's emmc artifact. The bundle's NVIDIA host tools
// run once inside a linux/amd64 Docker container (prep); the flash itself —
// stage-1 RCM boot plus the initrd's USB mass-storage exchange — is native.
// macOS and Linux.
func installOrin(ctx context.Context, version string, nightly, force bool) error {
	cacheDir, err := osCacheDir()
	if err != nil {
		return fmt.Errorf("resolving cache dir: %w", err)
	}

	plan, err := planOrinBundle(cacheDir, version, nightly)
	if err != nil {
		return err
	}

	// Brief the user on cabling / recovery mode, then confirm before scanning.
	if err := confirmOrinReady(plan.version); err != nil {
		return err
	}

	dev, err := pickRecoveryDevice("AGX Orin", "the USB-C port next to the 40-pin header",
		rcm.RecoveryDevice.IsOrin)
	if err != nil {
		return err
	}
	fmt.Printf("\n%s %s\n", tui.Dim("Target:"), tui.Device(dev.Describe()))

	if !force {
		fmt.Println()
		fmt.Println(tui.WarningMessage("This erases the QSPI boot flash + onboard eMMC of the AGX Orin. This cannot be undone."))
		ok, err := tui.ConfirmNoDefaultDanger(fmt.Sprintf("Flash %s?", dev.Describe()))
		if errors.Is(err, tui.ErrCancelled) || (err == nil && !ok) {
			return ErrUserCancelled
		}
		if err != nil {
			return err
		}
	}

	// Raw block writes to the exported disks run as root (sudo re-exec of the
	// hidden __t234-write helper); authenticate up front, and keep the sudo
	// timestamp warm across the multi-minute download + write.
	if err := preAuthElevation(); err != nil {
		return err
	}
	elevationCtx, cancelElevation := context.WithCancel(ctx)
	defer cancelElevation()
	keepElevationAlive(elevationCtx)

	logPath := ""
	if dir, derr := config.LogDir(); derr == nil {
		logPath = filepath.Join(dir, "orin-flash-"+time.Now().Format("20060102-150405")+".log")
	}

	var (
		bundleDir   string
		flashPlan   *t234.Plan
		finalStatus *t234.FinalStatus
	)
	stage2 := func(out io.Writer, detail func(string)) *t234.Stage2 {
		return &t234.Stage2{
			BundleDir: bundleDir,
			Plan:      flashPlan,
			Out:       out,
			Detail:    detail,
			RunHelper: runT234Helper,
		}
	}
	steps := []flashStep{
		{id: orinStepDownload, label: "Download flash bundle", run: func(out io.Writer, detail func(string)) (bool, error) {
			dir, cached, err := resolveOrinBundle(cacheDir, plan, detail)
			bundleDir = dir
			return cached, err
		}},
		{id: orinStepPrep, label: "Prepare flash workspace (Docker)", run: func(out io.Writer, detail func(string)) (bool, error) {
			detail("signing boot chain in a linux/amd64 container")
			cached, err := t234.Prep(bundleDir, out)
			if err != nil {
				return false, err
			}
			flashPlan, err = t234.LoadPlan(bundleDir)
			return cached, err
		}},
		{id: orinStepRCMBoot, label: "Stage 1  RCM boot", run: func(out io.Writer, _ func(string)) (bool, error) {
			boot, err := t234.ParseRCMBootCmd(bundleDir)
			if err != nil {
				return false, err
			}
			return false, bringup.Run(bringup.Options{
				Dir:        boot.Dir,
				MemBCT:     boot.MemBCT,
				DevicePath: dev.PathKey,
				SendOrder:  boot.SendOrder,
				Out:        out,
			})
		}},
		{id: orinStepCommands, label: "Stage 2  send flash commands", run: func(out io.Writer, detail func(string)) (bool, error) {
			return false, stage2(out, detail).SendFlashPackage(ctx)
		}},
		{id: orinStepWrite, label: "Stage 2  write eMMC partitions", run: func(out io.Writer, detail func(string)) (bool, error) {
			return false, stage2(out, detail).WriteRootfsDevice(ctx)
		}},
		{id: orinStepStatus, label: "Stage 2  device status", run: func(out io.Writer, detail func(string)) (bool, error) {
			var err error
			finalStatus, err = stage2(out, detail).AwaitFinalStatus(ctx)
			if err != nil {
				return false, err
			}
			saveOrinDeviceLogs(out, logPath, finalStatus)
			if !finalStatus.Success {
				return false, fmt.Errorf("device reported flash status %q", finalStatus.Status)
			}
			return false, nil
		}},
	}

	failedID, err := runFlashSteps(fmt.Sprintf("Flashing WendyOS %s", plan.version), steps)
	if err != nil {
		switch {
		case errors.Is(err, tui.ErrCancelled):
			return ErrUserCancelled
		case errors.Is(err, t234.ErrDockerMissing):
			fmt.Println()
			fmt.Println(tui.WarningMessage("Docker is required to prepare the Orin flash workspace (it runs NVIDIA's x86-64 signing tools). Start Docker and re-run — the device wasn't modified."))
		case errors.Is(err, rcm.ErrUSBAccess):
			fmt.Println("\n" + usbAccessHintBox())
		case errors.Is(err, t234.ErrDeviceSideFailed) || failedID == orinStepWrite || failedID == orinStepStatus:
			printOrinBadStateHint(os.Stdout)
		case failedID == orinStepRCMBoot || failedID == orinStepCommands:
			fmt.Println()
			fmt.Println(tui.WarningMessage("The flash failed before any data was written — the Orin wasn't modified. Re-enter recovery mode and try again."))
		}
		return err
	}

	fmt.Println(tui.SuccessMessage(fmt.Sprintf("Flashed WendyOS %s — the AGX Orin reboots into it by itself.", plan.version)))
	return nil
}

// orinFlashPlan is the resolved eMMC bundle to install and whether it is
// already cached. info is set only when a download is needed.
type orinFlashPlan struct {
	version string
	cached  bool
	info    *orinBundleInfo
}

// orinBundleTarPath is the cached .tegraflash-tar location for a version.
func orinBundleTarPath(version string) (string, error) {
	return osCachedPath(orinDeviceType, version, "emmc", ".tegraflash-tar")
}

// orinBundleDir is the extracted bundle directory for a version.
func orinBundleDir(cacheDir, version string) string {
	return filepath.Join(cacheDir, fmt.Sprintf("%s-%s-emmc", orinDeviceType, version))
}

// planOrinBundle resolves the target version and cache state without touching
// the network when a specific, already-cached version is requested.
func planOrinBundle(cacheDir, version string, nightly bool) (orinFlashPlan, error) {
	if version != "" && orinBundleCached(cacheDir, version) {
		return orinFlashPlan{version: version, cached: true}, nil
	}
	info, err := getOrinEMMCInfo(version, nightly)
	if err != nil {
		if version != "" {
			return orinFlashPlan{}, fmt.Errorf("flash bundle %s not in cache and manifest lookup failed: %w", version, err)
		}
		return orinFlashPlan{}, err
	}
	if orinBundleCached(cacheDir, info.Version) {
		return orinFlashPlan{version: info.Version, cached: true}, nil
	}
	return orinFlashPlan{version: info.Version, info: info}, nil
}

// orinBundleCached reports whether an extracted tree or downloaded tar for
// version is present.
func orinBundleCached(cacheDir, version string) bool {
	if _, err := os.Stat(filepath.Join(orinBundleDir(cacheDir, version), ".env.initrd-flash")); err == nil {
		return true
	}
	if tar, err := orinBundleTarPath(version); err == nil {
		if _, err := os.Stat(tar); err == nil {
			return true
		}
	}
	return false
}

// resolveOrinBundle downloads (when not cached), verifies, and extracts the
// eMMC bundle, returning the extracted dir and whether everything was a
// cache hit.
func resolveOrinBundle(cacheDir string, plan orinFlashPlan, detail func(string)) (string, bool, error) {
	dir := orinBundleDir(cacheDir, plan.version)
	if _, err := os.Stat(filepath.Join(dir, ".env.initrd-flash")); err == nil {
		return dir, true, nil
	}
	tarPath, err := orinBundleTarPath(plan.version)
	if err != nil {
		return "", false, err
	}
	cached := true
	if _, err := os.Stat(tarPath); err != nil {
		if plan.info == nil {
			return "", false, fmt.Errorf("flash bundle for %s is not cached and no download source was resolved", plan.version)
		}
		cached = false
		img := &imageInfo{DownloadURL: plan.info.URL, ImageSize: plan.info.SizeBytes, Version: plan.version}
		tmp, err := downloadImageInto(img, throttledDetail(detail, byteProgress))
		if err != nil {
			return "", false, fmt.Errorf("downloading flash bundle: %w", err)
		}
		if plan.info.Checksum != "" {
			detail("verifying")
			if err := verifySHA256(tmp, plan.info.Checksum); err != nil {
				os.Remove(tmp)
				return "", false, err
			}
		}
		if err := os.Rename(tmp, tarPath); err != nil {
			os.Remove(tmp)
			return "", false, fmt.Errorf("caching flash bundle: %w", err)
		}
	}
	detail("extracting")
	if err := t234.ExtractBundle(tarPath, dir); err != nil {
		return "", false, fmt.Errorf("extracting flash bundle: %w", err)
	}
	// The tar is large and its content now lives extracted next to it; drop
	// it to halve the cache footprint (the extracted tree is the cache).
	os.Remove(tarPath)
	return dir, cached, nil
}

// runT234Helper re-execs this binary as `sudo wendy __t234-write <args>`,
// relaying PROGRESS lines to onProgress. It is the Stage2.RunHelper hook.
func runT234Helper(args []string, onProgress func(done, total int64)) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating wendy binary: %w", err)
	}
	cmd := exec.Command("sudo", append([]string{self, "__t234-write"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting t234 write helper: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		var done, total int64
		if n, _ := fmt.Sscanf(sc.Text(), "PROGRESS %d %d", &done, &total); n == 2 && onProgress != nil {
			onProgress(done, total)
		}
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%s: %w", msg, err)
		}
		return err
	}
	return nil
}

// saveOrinDeviceLogs writes the device-side flash logs next to the host log
// and summarizes them into the step output.
func saveOrinDeviceLogs(out io.Writer, logPath string, st *t234.FinalStatus) {
	if len(st.Logs) == 0 {
		return
	}
	dir := strings.TrimSuffix(logPath, ".log") + "-device-logs"
	if logPath == "" {
		var err error
		if dir, err = os.MkdirTemp("", "orin-device-logs-*"); err != nil {
			return
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	names := make([]string, 0, len(st.Logs))
	for name := range st.Logs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_ = os.WriteFile(filepath.Join(dir, filepath.Base(name)), st.Logs[name], 0o644)
	}
	fmt.Fprintf(out, "Device-side logs (%s): %s\n", strings.Join(names, ", "), dir)
}

// confirmOrinReady prints a titled recovery-mode briefing and asks the user
// to confirm the target Orin is connected and in recovery mode.
func confirmOrinReady(version string) error {
	fmt.Println()
	fmt.Println(tui.Header("Flashing WendyOS " + version))
	fmt.Println(orinRecoveryBriefingBox())
	fmt.Println()
	ok, err := tui.Confirm("Is the target AGX Orin connected and in recovery mode?")
	if errors.Is(err, tui.ErrCancelled) || (err == nil && !ok) {
		return ErrUserCancelled
	}
	return err
}

// orinRecoveryBriefingBox renders the cabling and recovery-mode steps for the
// AGX Orin devkit (same style as the Thor briefing).
func orinRecoveryBriefingBox() string {
	section := func(title string) string {
		return briefMarker.Render("●") + " " + briefTitle.Render(title)
	}
	step := func(n int, text string) string {
		return "    " + briefNum.Render(fmt.Sprintf("%d.", n)) + " " + text
	}
	lines := []string{
		section("Storage"),
		"  WendyOS installs to the AGX Orin's " + briefKey.Render("onboard eMMC") + " and QSPI boot flash —",
		"  it uses no external drive. Anything on the eMMC (e.g. factory JetPack) is erased.",
		"",
		section("Requirements"),
		"  " + briefPort.Render("Docker") + " must be running: the flash workspace is prepared once inside a",
		"  linux/amd64 container (NVIDIA's signing tools are x86-64 Linux binaries).",
		"",
		section("USB-C cabling"),
		"  Connect this computer to the " + briefPort.Render("USB-C port next to the 40-pin header") + ".",
		"",
		section("Entering recovery mode"),
		"  Buttons, left → right:  " +
			briefKey.Render("Power") + briefDim.Render(" · ") +
			briefKey.Render("Force Recovery") + briefDim.Render(" · ") +
			briefKey.Render("Reset"),
		"",
		step(1, "Plug in power."),
		step(2, "Hold "+briefKey.Render("Force Recovery")+" (middle); briefly tap "+briefKey.Render("Reset")+" (right),"),
		"       then release " + briefKey.Render("Force Recovery") + ".",
		step(3, "Connect the "+briefPort.Render("USB-C port next to the 40-pin header")+" to this computer."),
	}
	return briefBorder.Render(strings.Join(lines, "\n"))
}

// printOrinBadStateHint prints a recovery notice for an Orin whose flash was
// interrupted after writing began. Unlike a half-flashed Thor there is no
// UEFI-variable dance: re-entering recovery and re-flashing recovers it.
func printOrinBadStateHint(w io.Writer) {
	body := strings.Join([]string{
		thorHintTitle.Render("⚠  Flashing was interrupted — the Orin may not boot"),
		"",
		"The eMMC or QSPI boot flash may be partially written. To recover:",
		"re-enter recovery mode (hold " + thorHintEmph.Render("Force Recovery") + ", tap " + thorHintEmph.Render("Reset") + ") and",
		"re-run " + thorHintCmd.Render("wendy os install --device-type jetson-agx-orin --storage emmc") + ".",
	}, "\n")
	fmt.Fprintln(w, "\n"+thorHintBorder.Render(body))
}
