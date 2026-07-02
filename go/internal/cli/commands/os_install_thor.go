//go:build darwin || linux

package commands

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/bringup"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/flasher"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/flashpack"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/rcm"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/shim"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// Step IDs for the flashing progress list.
const (
	stepDownload = iota
	stepStage1
	stepStage2
)

// installThor flashes a Jetson AGX Thor (T264) over USB recovery. It plans the
// flashpack (version + cache state) from the manifest, briefs and confirms with
// the user, then runs download → stage-1 RCM boot → stage-2 ADB partition flash
// as a live BuildKit-style step list whose verbose output surfaces only on
// failure. macOS and Linux.
func installThor(ctx context.Context, version string, nightly, force bool) error {
	cacheDir, err := osCacheDir()
	if err != nil {
		return fmt.Errorf("resolving cache dir: %w", err)
	}

	// Resolve which flashpack to use and whether it's cached, but don't download
	// or extract yet — we do that inside the progress UI, after the user commits.
	plan, err := planThorFlashpack(cacheDir, version, nightly)
	if err != nil {
		return err
	}

	// Brief the user on cabling / recovery mode, then confirm before scanning.
	if err := confirmThorReady(plan.version); err != nil {
		return err
	}

	// Pick the device to flash.
	dev, err := pickRecoveryDevice()
	if err != nil {
		return err
	}
	fmt.Printf("\n%s %s\n", tui.Dim("Target:"), tui.Device(dev.Describe()))

	if !force {
		fmt.Println()
		fmt.Println(tui.WarningMessage("This erases QSPI + internal NVMe on the Thor. This cannot be undone."))
		ok, err := tui.ConfirmNoDefaultDanger(fmt.Sprintf("Flash %s?", dev.Describe()))
		if errors.Is(err, tui.ErrCancelled) || (err == nil && !ok) {
			return ErrUserCancelled
		}
		if err != nil {
			return err
		}
	}

	// bootburn's full output goes to a conventional logs dir, not the terminal.
	logPath := ""
	if dir, derr := config.LogDir(); derr == nil {
		logPath = filepath.Join(dir, "thor-flash-"+time.Now().Format("20060102-150405")+".log")
	}

	// fp/shimDir are populated by the download and stage-1 steps and read by
	// later steps; the steps run sequentially in one goroutine, so no locking.
	var (
		fp      *flashpack.Flashpack
		shimDir string
	)
	steps := []flashStep{
		{id: stepDownload, label: "Download flashpack", run: func(out io.Writer, detail func(string)) (bool, error) {
			resolved, cached, err := downloadAndExtractFlashpack(cacheDir, plan, detail)
			fp = resolved
			return cached, err
		}},
		{id: stepStage1, label: "Stage 1  RCM boot", run: func(out io.Writer, detail func(string)) (bool, error) {
			// The adb/lsusb/timeout shim is set up here (device untouched until
			// bringup) so a shim failure aborts before we boot the payload.
			var serr error
			if shimDir, serr = setupADBShim(fp); serr != nil {
				return false, serr
			}
			return false, bringup.Run(bringup.Options{
				Dir:        fp.Stage1Dir(),
				MemBCT:     fp.MemBCT(),
				DevicePath: dev.PathKey,
				SendOrder:  fp.Manifest.Stage1SendOrder,
				Out:        out,
			})
		}},
		{id: stepStage2, label: "Stage 2  flash partitions", run: func(out io.Writer, _ func(string)) (bool, error) {
			return false, flasher.Run(flasher.Options{
				BundleDir:    fp.BundleDir(),
				WorkspaceDir: fp.WorkspaceOutDir(),
				ADBDir:       shimDir,
				ADBPort:      dev.PathKey, // pin the flashing gadget to the selected device
				LogPath:      logPath,
				PyYAMLDir:    fp.PyYAMLDir(),
				Out:          out,
			})
		}},
	}

	// A running Google adb server (Android platform-tools) claims every ADB device
	// it sees — including the Thor flashing gadget — exclusively, which makes our
	// own serverless adb fail to claim the interface ("bad access"). Stop it first.
	if stopConflictingADBServer() {
		fmt.Println(tui.InfoMessage("Stopped a running adb server (it would hold the Thor's flashing gadget)."))
	}

	failedID, err := runFlashSteps(fmt.Sprintf("Flashing WendyOS %s", plan.version), steps)
	if shimDir != "" {
		os.RemoveAll(shimDir)
	}
	if err != nil {
		switch {
		case errors.Is(err, tui.ErrCancelled):
			return ErrUserCancelled
		case errors.Is(err, rcm.ErrUSBAccess):
			// Stage 1 re-opens the device (it can re-enumerate between the scan
			// and the flash); a denied re-open gets the same guidance as the scan.
			fmt.Println("\n" + usbAccessHintBox())
		case errors.Is(err, flasher.ErrGadgetUnreachable):
			// Died at ADB setup before any write — the Thor is untouched, so
			// advise a plain retry rather than a scary bad-state recovery.
			printThorGadgetUnreachableHint(os.Stdout)
		case failedID == stepStage2:
			// Partitions were being written when it failed; the Thor can be left
			// unbootable (UEFI-only). Guide the user through recovering it.
			printThorBadStateHint(os.Stdout)
		case failedID == stepStage1:
			// RCM boot failed before any write — device untouched.
			fmt.Println()
			fmt.Println(tui.WarningMessage("RCM boot failed — the Thor wasn't modified. Re-enter recovery mode and try again."))
		}
		return err
	}

	fmt.Println(tui.SuccessMessage(fmt.Sprintf("Flashed WendyOS %s — power-cycle the Thor out of recovery to boot it.", plan.version)))
	return nil
}

// thorFlashPlan is the resolved flashpack to install and whether it is already
// cached. info is set (download source + checksum) only when a download is needed.
type thorFlashPlan struct {
	version string
	cached  bool
	info    *thorFlashpackInfo
}

// planThorFlashpack resolves the target version and cache state without touching
// the network when a specific, already-cached version is requested. On a cache
// miss it consults the manifest for the version and download source. An empty
// version means the manifest's latest (or latest nightly).
func planThorFlashpack(cacheDir, version string, nightly bool) (thorFlashPlan, error) {
	if version != "" && flashpackCached(cacheDir, version) {
		return thorFlashPlan{version: version, cached: true}, nil
	}

	info, err := getThorFlashpackInfo(version, nightly)
	if err != nil {
		if version != "" {
			return thorFlashPlan{}, fmt.Errorf("flashpack %s not in cache and manifest lookup failed: %w", version, err)
		}
		return thorFlashPlan{}, err
	}
	if flashpackCached(cacheDir, info.Version) {
		return thorFlashPlan{version: info.Version, cached: true}, nil
	}
	return thorFlashPlan{version: info.Version, info: info}, nil
}

// flashpackCached reports whether an extracted tree or a downloaded .tar.zst for
// version is present, i.e. Resolve can proceed without a download.
func flashpackCached(cacheDir, version string) bool {
	if _, err := os.Stat(filepath.Join(cacheDir, flashpack.FlashpackName(version))); err == nil {
		return true
	}
	if _, err := os.Stat(flashpack.TarballCachePath(cacheDir, version)); err == nil {
		return true
	}
	return false
}

// downloadAndExtractFlashpack downloads the flashpack (when not cached),
// verifies it against the manifest checksum, and extracts it via Resolve,
// reporting live progress through detail. It returns the ready flashpack and
// whether the download was skipped (cache hit).
func downloadAndExtractFlashpack(cacheDir string, plan thorFlashPlan, detail func(string)) (*flashpack.Flashpack, bool, error) {
	if !plan.cached {
		img := &imageInfo{DownloadURL: plan.info.URL, ImageSize: plan.info.SizeBytes, Version: plan.version}
		tmp, err := downloadImageInto(img, throttledDetail(detail, byteProgress))
		if err != nil {
			return nil, false, fmt.Errorf("downloading flashpack: %w", err)
		}
		if plan.info.Checksum != "" {
			detail("verifying")
			if err := verifySHA256(tmp, plan.info.Checksum); err != nil {
				os.Remove(tmp)
				return nil, false, err
			}
		}
		if err := os.Rename(tmp, flashpack.TarballCachePath(cacheDir, plan.version)); err != nil {
			os.Remove(tmp)
			return nil, false, fmt.Errorf("caching flashpack: %w", err)
		}
	}
	detail("extracting")
	fp, err := flashpack.Resolve(cacheDir, plan.version)
	return fp, plan.cached, err
}

// setupADBShim materializes wendy's adb/lsusb/timeout shim on a temp PATH dir and
// replaces the flashpack's bundled adb binaries with it (bootburn calls adb by
// absolute path too). Returns the shim dir for the caller to clean up.
func setupADBShim(fp *flashpack.Flashpack) (string, error) {
	shimDir, err := shim.MaterializeADBDir()
	if err != nil {
		return "", fmt.Errorf("preparing adb shim: %w", err)
	}
	for _, p := range []string{
		filepath.Join(fp.BundleDir(), "unified_flash", "tools", "flashtools", "flash", "adb"),
		filepath.Join(fp.WorkspaceOutDir(), "tools", "flashtools", "flash", "adb"),
	} {
		if err := shim.LinkSelfAt(p); err != nil {
			os.RemoveAll(shimDir)
			return "", fmt.Errorf("installing adb shim at %s: %w", p, err)
		}
	}
	return shimDir, nil
}

// flashStep is one entry in the flashing progress list.
type flashStep struct {
	id    int
	label string
	// run performs the step. It writes verbose output to out (captured and shown
	// only on failure in interactive mode) and may call detail to update the live
	// trailing text (e.g. a byte count). It returns cached=true when the work was
	// skipped because a cache was warm.
	run func(out io.Writer, detail func(string)) (cached bool, err error)
}

// runFlashSteps runs steps in order, rendering them as a live BuildKit-style step
// list on an interactive terminal (verbose per-step output buffered and printed
// only if a step fails) or as concise streamed lines otherwise. It returns the id
// of the failing step (or -1 on success/cancel) and the error.
func runFlashSteps(title string, steps []flashStep) (int, error) {
	if !isInteractiveTerminal() {
		return runFlashStepsPlain(title, steps)
	}

	m := tui.NewStepsModel(title)
	prog := tui.NewProgressProgram(m)

	type outcome struct {
		failedID int
		err      error
		verbose  []byte
	}
	resC := make(chan outcome, 1)
	go func() {
		failedID, runErr := -1, error(nil)
		var failVerbose []byte
		for _, s := range steps {
			// Per-step buffer so a later failure surfaces only that step's output,
			// not the successful earlier steps.
			buf := &boundedBuffer{max: maxRawBuildCapture}
			prog.Send(tui.StepStartMsg{ID: s.id, Label: s.label})
			cached, err := s.run(buf, func(d string) { prog.Send(tui.StepDetailMsg{ID: s.id, Detail: d}) })
			if err != nil {
				prog.Send(tui.StepFailMsg{ID: s.id})
				failedID, runErr, failVerbose = s.id, err, buf.Bytes()
				break
			}
			prog.Send(tui.StepDoneMsg{ID: s.id, Cached: cached})
		}
		resC <- outcome{failedID, runErr, failVerbose}
		prog.Send(tui.StepsDoneMsg{Err: runErr})
	}()

	final, uiErr := prog.Run()
	if uiErr != nil {
		return -1, fmt.Errorf("flash progress UI: %w", uiErr)
	}
	if cancelErr := final.(tui.StepsModel).Err(); errors.Is(cancelErr, tui.ErrCancelled) {
		return -1, cancelErr
	}
	res := <-resC
	if res.err != nil {
		// The live UI suppressed the failing step's output; surface it now.
		os.Stdout.Write(res.verbose)
	}
	return res.failedID, res.err
}

// runFlashStepsPlain is the non-interactive fallback: a header per step with the
// step's verbose output streamed live (useful in CI, and safe against idle
// timeouts during the multi-minute flash).
func runFlashStepsPlain(title string, steps []flashStep) (int, error) {
	fmt.Println(title)
	for _, s := range steps {
		fmt.Printf("==> %s\n", s.label)
		cached, err := s.run(os.Stdout, func(string) {})
		if err != nil {
			return s.id, err
		}
		if cached {
			fmt.Println("    (cached)")
		}
	}
	return -1, nil
}

// throttledDetail wraps a byte-progress callback so it emits a formatted detail
// string at most every ~66ms, coalescing the flood from parallel download
// workers. Safe for concurrent callers.
func throttledDetail(detail func(string), format func(downloaded, total int64) string) func(downloaded, total int64) {
	var lastNanos atomic.Int64
	const minInterval = int64(66 * time.Millisecond)
	return func(downloaded, total int64) {
		now := time.Now().UnixNano()
		prev := lastNanos.Load()
		if now-prev < minInterval {
			return
		}
		if !lastNanos.CompareAndSwap(prev, now) {
			return
		}
		detail(format(downloaded, total))
	}
}

// byteProgress formats a download's progress like "1.2/3.0 GiB".
func byteProgress(downloaded, total int64) string {
	const gib = 1 << 30
	if total <= 0 {
		return fmt.Sprintf("%.1f GiB", float64(downloaded)/gib)
	}
	return fmt.Sprintf("%.1f/%.1f GiB", float64(downloaded)/gib, float64(total)/gib)
}

// verifySHA256 checks that path's SHA-256 matches the expected lowercase-hex digest.
func verifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hashing %s: %w", filepath.Base(path), err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("flashpack checksum mismatch: got %s, manifest says %s", got, want)
	}
	return nil
}

// pickRecoveryDevice lists Jetsons in recovery mode and selects one (auto when there
// is exactly one, interactive picker when there are several).
func pickRecoveryDevice() (rcm.RecoveryDevice, error) {
	for {
		devs, err := rcm.ListRecoveryDevices()
		switch {
		case errors.Is(err, rcm.ErrUSBAccess) && len(devs) == 0:
			// Nothing usable: explain the fix, then offer a rescan — the user can
			// install the udev rule (or replug; the ACL grant can race a fresh
			// plug-in) without restarting the flash.
			fmt.Println("\n" + usbAccessHintBox())
			fmt.Print("Press Enter to rescan, or 'q' to quit: ")
			if readQuit() {
				return rcm.RecoveryDevice{}, fmt.Errorf("cannot open the Jetson's USB device: permission denied")
			}
			continue
		case errors.Is(err, rcm.ErrUSBAccess):
			// Some boards opened, another was denied. Warn rather than proceed
			// silently: auto-select below could otherwise flash the wrong Thor.
			fmt.Println()
			fmt.Println(tui.WarningMessage("Another Jetson in recovery mode was detected but couldn't be opened (USB access denied) — it is NOT listed below."))
		case err != nil:
			return rcm.RecoveryDevice{}, err
		}
		switch len(devs) {
		case 0:
			// The user already confirmed the Thor is in recovery mode, so a zero
			// scan usually means cabling or the button sequence needs another try.
			// Offer a rescan instead of aborting.
			fmt.Print("\nNo Jetson found in USB recovery mode.\n" +
				"  Check the USB-C cable is in the port next to the HDMI port and re-do the\n" +
				"  recovery-mode button sequence, then rescan.\n" +
				"Press Enter to rescan, or 'q' to quit: ")
			if readQuit() {
				return rcm.RecoveryDevice{}, ErrUserCancelled
			}
			continue
		case 1:
			return devs[0], nil
		default:
			var items []tui.PickerItem
			byKey := make(map[string]rcm.RecoveryDevice, len(devs))
			for _, d := range devs {
				byKey[d.PathKey] = d
				items = append(items, tui.PickerItem{
					Name:        d.Describe(),
					Description: "",
					Section:     "Recovery devices",
					SortKey:     d.PathKey,
					Value:       d.PathKey,
				})
			}
			sel, err := pickFromItems("Select the Thor to flash", items)
			if err != nil {
				return rcm.RecoveryDevice{}, err
			}
			return byKey[sel], nil
		}
	}
}

// readQuit reads a single line and reports whether the user asked to quit
// (q/quit). Any other input — including a bare Enter — means "continue".
func readQuit() bool {
	s := bufio.NewScanner(os.Stdin)
	if !s.Scan() {
		return true // EOF (e.g. closed stdin) — don't loop forever
	}
	switch strings.ToLower(strings.TrimSpace(s.Text())) {
	case "q", "quit":
		return true
	default:
		return false
	}
}

// Styles for the pre-flash briefing. Color carries the hierarchy: emerald
// section headers, amber for things the user physically presses/disconnects,
// sky for cabling/ports, so the box scans rather than reading as a wall of text.
var (
	briefBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(tui.ColorBorder).
			Padding(1, 3)
	briefMarker = lipgloss.NewStyle().Foreground(tui.ColorAccent).Bold(true)
	briefTitle  = lipgloss.NewStyle().Foreground(tui.ColorPrimary).Bold(true)
	briefKey    = lipgloss.NewStyle().Foreground(tui.ColorNotice).Bold(true) // buttons / "disconnect"
	briefPort   = lipgloss.NewStyle().Foreground(tui.Sky500).Bold(true)      // cabling / ports
	briefNum    = lipgloss.NewStyle().Foreground(tui.ColorAccent).Bold(true)
	briefDim    = lipgloss.NewStyle().Foreground(tui.ColorDim)
)

// thorRecoveryBriefingBox renders the cabling and recovery-mode steps the user
// must complete before a Thor will appear in the USB recovery scan, as a colored,
// scannable box.
func thorRecoveryBriefingBox() string {
	section := func(title string) string {
		return briefMarker.Render("●") + " " + briefTitle.Render(title)
	}
	step := func(n int, text string) string {
		return "    " + briefNum.Render(fmt.Sprintf("%d.", n)) + " " + text
	}
	lines := []string{
		section("Storage"),
		"  WendyOS installs to the Thor's internal flash + NVMe — it uses no",
		"  external USB drive. " + briefKey.Render("Disconnect any USB drive now") + " and leave it out.",
		"",
		section("USB-C cabling"),
		"  Connect this computer to the " + briefPort.Render("USB-C port next to the HDMI port") + ".",
		"  The other USB-C port is " + briefDim.Render("power-only") + ".",
		"",
		section("Entering recovery mode"),
		"  Front buttons, left → right:  " +
			briefKey.Render("Power") + briefDim.Render(" · ") +
			briefKey.Render("Force Recovery") + briefDim.Render(" · ") +
			briefKey.Render("Reset"),
		"",
		step(1, "Start with the Thor "+briefDim.Render("unplugged and powered off")+"."),
		step(2, "Plug in power."),
		step(3, "Hold "+briefKey.Render("Force Recovery")+" (middle); briefly tap "+briefKey.Render("Reset")+" (right),"),
		"       then release " + briefKey.Render("Force Recovery") + ".",
		step(4, "Connect the "+briefPort.Render("USB-C port next to HDMI")+" to this computer."),
	}
	return briefBorder.Render(strings.Join(lines, "\n"))
}

// Styles for the interrupted-flash recovery notice. A red border + bold red
// title make it impossible to miss when a flash dies partway through.
var (
	thorHintBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(tui.ColorError).
			Padding(1, 3)
	thorHintTitle = lipgloss.NewStyle().Foreground(tui.ColorError).Bold(true)
	thorHintEmph  = lipgloss.NewStyle().Foreground(tui.ColorNotice).Bold(true)
	thorHintCmd   = lipgloss.NewStyle().Foreground(tui.Sky500).Bold(true)
)

// printThorBadStateHint prints a prominent recovery notice for a Thor whose
// flash was interrupted partway through. Such a Thor can be left booting only
// into the UEFI shell; zeroing both rootfs slot-status variables clears the
// "bad" marks so it will attempt a normal boot (or re-enter recovery) again.
func printThorBadStateHint(w io.Writer) {
	const guid = "781E084C-A330-417C-B678-38E696380CB9"
	cmdA := fmt.Sprintf("setvar RootfsStatusSlotA -guid %s -bs -rt -nv =0x00000000", guid)
	cmdB := fmt.Sprintf("setvar RootfsStatusSlotB -guid %s -bs -rt -nv =0x00000000", guid)

	body := strings.Join([]string{
		thorHintTitle.Render("⚠  Flashing was interrupted — the Thor may be in a bad state"),
		"",
		"Plug the Thor into a monitor and power-cycle it.",
		"If the screen shows " + thorHintEmph.Render("UEFI") + ", the Thor is in a bad state.",
		"",
		"Attach a USB keyboard and type these two commands exactly:",
		"",
		thorHintCmd.Render("  " + cmdA),
		thorHintCmd.Render("  " + cmdB),
		"",
		"Then power-cycle again, re-enter recovery mode, and re-run the flash.",
	}, "\n")

	fmt.Fprintln(w, "\n"+thorHintBorder.Render(body))
}

// stopConflictingADBServer stops a running Google adb server (Android
// platform-tools) at its default port. Such a server claims every ADB device it
// sees — including the Thor flashing gadget — exclusively, so wendy's own
// serverless adb then fails to claim the interface ("bad access [code -3]").
// Returns true if a server was contacted.
func stopConflictingADBServer() bool {
	return stopADBServer("127.0.0.1:5037")
}

// stopADBServer sends the adb host "kill" command to an adb server at addr,
// stopping it without depending on which adb binary (or version) is installed.
// It is a no-op (returns false) when nothing is listening.
func stopADBServer(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false // no server listening
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	// adb host protocol: a 4-hex-digit length prefix followed by the request.
	if _, err := conn.Write([]byte("0009host:kill")); err != nil {
		return false
	}
	_, _ = io.ReadFull(conn, make([]byte, 4)) // drain OKAY/FAIL; the server exits either way
	return true
}

// printThorGadgetUnreachableHint prints a calm, non-alarming note for a flash
// that died at ADB setup before writing anything: the Thor is untouched, so the
// fix is a plain retry (the gadget can re-enumerate on a different USB port).
func printThorGadgetUnreachableHint(w io.Writer) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, tui.WarningMessage("Couldn't reach the Thor's flashing gadget over USB — nothing was written, so the Thor is safe."))
	fmt.Fprintln(w, tui.Dim("  A running adb server can hold the gadget; if you have Android platform-tools,"))
	fmt.Fprintln(w, tui.Dim("  run `adb kill-server`. Otherwise unplug/replug the USB-C cable (the port next to"))
	fmt.Fprintln(w, tui.Dim("  HDMI), re-enter recovery mode, and flash again."))
}

// The udev rule that grants users access to NVIDIA Jetson USB devices (recovery
// mode 0955:7023/7026 and the initrd-flash ADB gadget 0955:7100). The deb/rpm
// packages install the same rule from packaging/linux/udev/70-wendy-jetson.rules;
// a test asserts the two copies stay identical (a stale copy here would have
// users write an /etc rule that overrides the packaged /usr/lib one).
const (
	usbUdevRulePath = "/etc/udev/rules.d/70-wendy-jetson.rules"
	usbUdevRule     = `SUBSYSTEM=="usb", ATTRS{idVendor}=="0955", MODE="0660", GROUP="plugdev", TAG+="uaccess"`
)

// usbAccessHintBox explains how to regain USB access to the Jetson when the OS
// refuses to open it: on Linux, udev permissions; on macOS, almost always
// another process holding the device.
func usbAccessHintBox() string {
	lines := []string{
		thorHintTitle.Render("⚠  USB access denied"),
		"",
		"A Jetson is in recovery mode, but the OS refused wendy access to its USB device.",
	}
	if runtime.GOOS == "linux" {
		lines = append(lines,
			"Grant access with a udev rule (one-time; the wendy deb/rpm packages install it):",
			"",
			thorHintCmd.Render("  echo '"+usbUdevRule+"' \\"),
			thorHintCmd.Render("    | sudo tee "+usbUdevRulePath),
			thorHintCmd.Render("  sudo udevadm control --reload-rules && sudo udevadm trigger"),
			"",
			"Add your user to the "+thorHintEmph.Render("plugdev")+" group — if your distro has none, create it",
			"("+thorHintCmd.Render("sudo groupadd plugdev && sudo usermod -aG plugdev $USER")+", then log in",
			"again) — replug the USB-C cable and rescan. Or re-run the flash with sudo.",
		)
	} else {
		lines = append(lines,
			"Another process is likely holding it (another flashing tool, a VM with USB",
			"passthrough, or another wendy). Quit it, unplug/replug the USB-C cable, and",
			"rescan.",
		)
	}
	return thorHintBorder.Render(strings.Join(lines, "\n"))
}

// confirmThorReady prints a titled recovery-mode briefing and asks the user to
// confirm the target Thor is connected and in recovery mode before scanning.
// Returns ErrUserCancelled if the user declines or cancels.
func confirmThorReady(version string) error {
	fmt.Println()
	fmt.Println(tui.Header("Flashing WendyOS " + version))
	fmt.Println(thorRecoveryBriefingBox())
	fmt.Println()
	ok, err := tui.Confirm("Is the target Thor connected and in recovery mode?")
	if errors.Is(err, tui.ErrCancelled) || (err == nil && !ok) {
		return ErrUserCancelled
	}
	return err
}
