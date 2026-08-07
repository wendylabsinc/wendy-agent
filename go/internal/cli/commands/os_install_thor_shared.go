package commands

// Cross-platform Jetson AGX Thor (T264) USB-recovery install flow. The stage-2
// partition flash (package flashengine) is shared across Windows, macOS and
// Linux; only stage-1 RCM boot, recovery-device enumeration, and the ADB
// transport differ per platform, provided by the thor*Host hooks implemented in
// os_install_thor_hw_{unix,windows}.go.

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
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/lipgloss"
	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/flashengine"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/flashpack"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/wendyconf"
)

// Step IDs for the flashing progress list.
const (
	stepDownload = iota
	stepProvision
	stepStage1
	stepStage2
)

// errGadgetUnreachable marks a stage-2 failure where the flashing gadget never
// appeared over USB. Nothing was written when this happens, so the Thor is
// untouched — the caller shows the calm "gadget unreachable" hint rather than the
// alarming "bad state" recovery box. The platform thorOpenGadget hooks wrap it.
var errGadgetUnreachable = errors.New("thor flashing gadget did not appear over USB")

// thorDevice identifies a selected Jetson in recovery mode, platform-neutrally.
// PathKey is the stable physical-location key used to re-find the device across
// the RCM→gadget re-enumeration; Label is a human description.
type thorDevice struct {
	PathKey string
	Label   string
}

// installThor flashes a Jetson AGX Thor over USB recovery: plan the flashpack,
// brief + confirm, prepare the host (Windows installs the WinUSB driver), pick
// the device, then run download → stage-1 RCM boot → stage-2 partition flash as a
// BuildKit-style step list. Stage-2 is the shared Go engine (flashengine) on all
// platforms.
func installThor(ctx context.Context, version string, nightly, force bool, wifi wifiCLIOptions, deviceName string, preOpts preEnrollOptions, prNumber int) error {
	// Thor's USB recovery access is an in-process libusb handle, so the whole
	// process must be root on macOS/Linux — caching the sudo timestamp is not
	// enough. Elevate up front, before the briefing, so a missing-permission
	// failure never surprises the user mid-flash (WDY-1843). On a successful sudo
	// re-exec this replaces the process and does not return. Windows elevates
	// separately via UAC when it installs the WinUSB driver (thorPrepareHost).
	if err := ensureThorRootAccess(); err != nil {
		return err
	}

	cacheDir, err := osCacheDir()
	if err != nil {
		return fmt.Errorf("resolving cache dir: %w", err)
	}

	plan, err := planThorFlashpack(cacheDir, version, nightly, prNumber)
	if err != nil {
		return err
	}
	if plan.offline {
		fmt.Println(tui.WarningMessage(fmt.Sprintf("Offline — using cached WendyOS %s; cannot confirm it is the latest build.", plan.version)))
	}
	if plan.refreshed {
		fmt.Println(tui.Dim(fmt.Sprintf("Cached WendyOS %s is outdated; downloading the current build.", plan.version)))
	}

	// Fail fast on a full disk, before the user starts cabling the Thor.
	if err := checkThorDiskSpace(cacheDir, plan); err != nil {
		return err
	}

	// Resolve provisioning up front: interactive prompts and pre-enrollment run
	// before the flash UI takes over the terminal, and a bad flag or failed
	// enrollment aborts before we touch USB. Written to the config image in
	// stepProvision, then flashed like a disk install's config partition.
	creds, err := resolveWiFiCredentialsList(wifi)
	if err != nil {
		return err
	}
	name, err := resolveDeviceName(deviceName)
	if err != nil {
		return err
	}
	provJSON, err := resolveProvisioningJSON(ctx, preOpts, name)
	if err != nil {
		return err
	}

	// Brief the user (Windows briefing includes the one-time WinUSB driver note),
	// then confirm before touching USB.
	if err := confirmThorReady(plan.version, force); err != nil {
		return err
	}

	// Prepare the host: Windows installs+trusts the WinUSB driver (UAC); macOS/
	// Linux stop any conflicting adb server that would claim the gadget.
	if err := thorPrepareHost(os.Stdout); err != nil {
		return err
	}

	dev, err := pickThorRecoveryDevice()
	if err != nil {
		return err
	}
	fmt.Printf("\n%s %s\n", tui.Dim("Target:"), tui.Device(dev.Label))

	if !force {
		fmt.Println()
		fmt.Println(tui.WarningMessage("This erases QSPI + internal NVMe on the Thor. This cannot be undone."))
		ok, err := tui.ConfirmNoDefaultDanger(fmt.Sprintf("Flash %s?", dev.Label))
		if errors.Is(err, tui.ErrCancelled) || (err == nil && !ok) {
			return ErrUserCancelled
		}
		if err != nil {
			return err
		}
	}

	// flashCtx aborts in-flight step work (the stage-2 engine) when the user
	// confirms a ctrl+c cancel in the steps UI.
	flashCtx, cancelFlash := context.WithCancel(ctx)
	defer cancelFlash()

	// Persist the full flash output to a log file (in addition to whatever the UI
	// shows) so a failure has a durable post-mortem/support artifact. Best-effort:
	// a log we can't open never blocks the flash.
	var logW io.Writer = io.Discard
	var logPath string
	if dir, derr := config.LogDir(); derr == nil {
		logPath = filepath.Join(dir, "thor-flash-"+time.Now().Format("20060102-150405")+".log")
		if lf, lerr := os.Create(logPath); lerr == nil {
			defer lf.Close()
			fmt.Fprintf(lf, "wendy os install — Jetson AGX Thor — WendyOS %s\n\n", plan.version)
			logW = lf
			defer func() { fmt.Println(tui.Dim("Full flash log: " + logPath)) }()
		} else {
			logPath = ""
		}
	}

	var (
		fp             *flashpack.Flashpack
		runWorkspace   string
		runConfigImage string
	)
	defer func() {
		if runWorkspace != "" {
			_ = os.RemoveAll(runWorkspace)
		}
	}()
	steps := []flashStep{
		{id: stepDownload, label: "Download flashpack", run: func(out io.Writer, detail func(string)) (bool, error) {
			resolved, cached, err := downloadAndExtractFlashpack(cacheDir, plan, detail)
			fp = resolved
			return cached, err
		}},
		{id: stepProvision, label: "Write config partition", run: func(out io.Writer, detail func(string)) (bool, error) {
			sourceImages := filepath.Join(fp.FlashWorkspaceDir(), "flash-images")
			var err error
			runWorkspace, runConfigImage, err = prepareMutableWorkspace(sourceImages, filepath.Join(sourceImages, "config-partition.fat32.img"))
			if err != nil {
				return false, err
			}
			return false, injectConfigPartition(runConfigImage, creds, name, provJSON, out, detail)
		}},
		{id: stepStage1, label: "Stage 1  RCM boot", run: func(out io.Writer, _ func(string)) (bool, error) {
			return false, thorStageOne(fp, dev, out)
		}},
		{id: stepStage2, label: "Stage 2  flash partitions",
			abortWarning: "Partitions are being written — aborting now can leave the Thor unbootable. Press ctrl+c again to abort anyway.",
			run: func(out io.Writer, detail func(string)) (bool, error) {
				transport, closer, err := thorOpenGadget(dev, out)
				if err != nil {
					return false, err
				}
				// The gadget is owned entirely by this worker goroutine: close it
				// here when the step returns, never from the main goroutine. On a
				// ctrl+c cancel the worker can outlive runFlashSteps' grace window,
				// and closing the transport from main would race with (and close it
				// under) an in-flight USB transfer.
				defer closer()
				return false, flashengine.Run(flashCtx, transport, flashengine.Options{
					FlashImagesDir: runWorkspace,
					NvddLocalPath:  filepath.Join(fp.BundleDir(), "unified_flash", "tools", "flashtools", "flash", "nvdd"),
					Out:            out,
					// USB-push progress, e.g. "38% · 6.9/18.1 GiB". Tracks
					// transfers only, so it pauses during device-side writes
					// and verification.
					Progress: throttledDetail(detail, byteProgress),
				})
			}},
	}

	failedID, err := runFlashSteps(fmt.Sprintf("Flashing WendyOS %s", plan.version), steps, cancelFlash, logW)
	if err != nil {
		switch {
		case errors.Is(err, tui.ErrCancelled):
			if failedID == stepStage2 {
				// The abort interrupted partition writes; the Thor can be left
				// unbootable exactly like a stage-2 failure. Show the recovery
				// guide instead of exiting silently.
				printThorBadStateHint(os.Stdout)
			}
			return ErrUserCancelled
		case thorIsUSBAccessErr(err):
			// Stage 1 re-opens the device (it can re-enumerate between the scan
			// and the flash); a denied re-open gets the same guidance as the scan.
			fmt.Println("\n" + usbAccessHintBox())
		case errors.Is(err, errGadgetUnreachable):
			// Stage-1 booted but the gadget never re-enumerated; nothing was written.
			printThorGadgetUnreachableHint(os.Stdout)
		case failedID == stepStage2:
			// Partitions were being written; the Thor can be left unbootable.
			printThorBadStateHint(os.Stdout)
		case failedID == stepStage1:
			fmt.Println()
			fmt.Println(tui.WarningMessage("RCM boot failed — the Thor wasn't modified. Re-enter recovery mode and try again."))
		}
		return err
	}

	fmt.Println(tui.SuccessMessage(fmt.Sprintf("Flashed WendyOS %s — power-cycle the Thor out of recovery to boot it. (press the right button once)", plan.version)))
	return nil
}

// fatWriter writes config-partition files into a mounted FAT32 filesystem image
// (Thor's config-partition.fat32.img) — the analog of dirTarget for a drive.
type fatWriter struct{ fs filesystem.FileSystem }

func (w fatWriter) WriteFile(name string, data []byte, _ os.FileMode) error {
	f, err := w.fs.OpenFile("/"+name, os.O_CREATE|os.O_RDWR|os.O_TRUNC) // FAT has no perms; O_TRUNC keeps re-runs clean
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

// injectConfigPartition populates the flashpack's FAT32 config image with the
// same files a disk install writes to a drive's config partition — agent binary,
// wendy.conf, provisioning.json, clock_floor — so first boot applies them via the
// identical on-device path. Runs before stage-2, so a failure aborts with nothing
// written to the Thor.
func injectConfigPartition(img string, creds []wendyconf.WifiCredential, deviceName string, provJSON []byte, out io.Writer, detail func(string)) error {
	// Freshen the agent like a disk install, but best-effort: resolveAgentBinary is
	// the only network step here, so an offline re-flash of a cached flashpack still
	// provisions wifi/enrollment, falling back to the agent baked into the image.
	detail("downloading agent")
	agentBinary, agentVer, _, err := resolveAgentBinary("arm64", false)
	if err != nil {
		fmt.Fprintf(out, "warning: could not download wendy-agent (%v); using the agent baked into the image\n", err)
	} else {
		detail("agent " + agentVer)
	}

	d, err := diskfs.Open(img)
	if err != nil {
		return fmt.Errorf("opening config image: %w", err)
	}
	defer d.Close()
	fs, err := d.GetFilesystem(0) // bare FAT32, no partition table
	if err != nil {
		return fmt.Errorf("reading config filesystem: %w", err)
	}
	return writeConfigFilesTo(fatWriter{fs}, agentBinary, creds, deviceName, provJSON)
}

// ---- flashpack plan / download (cross-platform) ----

// thorFlashPlan is the resolved flashpack to install and whether it is cached.
type thorFlashPlan struct {
	version   string
	cached    bool
	offline   bool // resolved from cache without reaching the manifest (unverifiable)
	refreshed bool // a stale cached copy was purged; a fresh download will run
	info      *thorFlashpackInfo
}

// planThorFlashpack decides whether to reuse the cache or download. It always
// tries the manifest first so it can compare the published checksum against the
// cached copy's provenance stamp: a --pr build keeps a stable "pr-N" version tag
// across re-pushes, so an existence-only cache check would silently flash an
// outdated build. On a checksum change it purges and re-downloads; when the
// manifest is unreachable it falls back to a nameable cached copy so an offline
// bench flash isn't blocked.
func planThorFlashpack(cacheDir, version string, nightly bool, pr int) (thorFlashPlan, error) {
	info, err := getThorFlashpackInfo(version, nightly, pr)
	if err != nil {
		if v := offlineThorVersion(version, pr); v != "" && flashpackCached(cacheDir, v) {
			return thorFlashPlan{version: v, cached: true, offline: true}, nil
		}
		if version != "" {
			return thorFlashPlan{}, fmt.Errorf("flashpack %s not in cache and manifest lookup failed: %w", version, err)
		}
		return thorFlashPlan{}, err
	}
	if flashpackCached(cacheDir, info.Version) {
		extracted := filepath.Join(cacheDir, flashpack.FlashpackName(info.Version))
		if flashpack.StampStale(extracted, info.Checksum) {
			flashpack.Purge(extracted, flashpack.TarballCachePath(cacheDir, info.Version))
			return thorFlashPlan{version: info.Version, refreshed: true, info: info}, nil
		}
		return thorFlashPlan{version: info.Version, cached: true}, nil
	}
	return thorFlashPlan{version: info.Version, info: info}, nil
}

// offlineThorVersion names the cached flashpack to try when the manifest can't be
// reached: the explicit --version, or a PR's conventional "pr-N" tag. Empty when
// neither applies (a bare latest/nightly needs the manifest to resolve a tag).
func offlineThorVersion(version string, pr int) string {
	if version != "" {
		return version
	}
	if pr > 0 {
		return fmt.Sprintf("pr-%d", pr)
	}
	return ""
}

func flashpackCached(cacheDir, version string) bool {
	if _, err := os.Stat(filepath.Join(cacheDir, flashpack.FlashpackName(version))); err == nil {
		return true
	}
	if _, err := os.Stat(flashpack.TarballCachePath(cacheDir, version)); err == nil {
		return true
	}
	return false
}

// thorExtractedFactor estimates the extracted flashpack tree from the compressed
// tarball size. Real flashpacks extract to ~2.1×; 2.5 leaves margin.
const thorExtractedFactor = 2.5

// thorFlashpackSpaceNeeded estimates the bytes about to be written under cacheDir:
// download + extraction, extraction only when the tarball is already cached, or 0
// when the extracted tree exists (or the size is unknown — never block on that).
func thorFlashpackSpaceNeeded(cacheDir string, plan thorFlashPlan) int64 {
	if _, err := os.Stat(filepath.Join(cacheDir, flashpack.FlashpackName(plan.version))); err == nil {
		return 0
	}
	if fi, err := os.Stat(flashpack.TarballCachePath(cacheDir, plan.version)); err == nil {
		return int64(float64(fi.Size()) * thorExtractedFactor)
	}
	if plan.info != nil && plan.info.SizeBytes > 0 {
		return int64(float64(plan.info.SizeBytes) * (1 + thorExtractedFactor))
	}
	return 0
}

// checkThorDiskSpace errors out early when the volume holding cacheDir doesn't
// have room for the flashpack download + extraction. Best-effort: an unknown
// size or an unreadable free-space figure never blocks the install.
func checkThorDiskSpace(cacheDir string, plan thorFlashPlan) error {
	needed := thorFlashpackSpaceNeeded(cacheDir, plan)
	if needed == 0 {
		return nil
	}
	avail, ok := diskAvailBytes(cacheDir)
	if !ok || avail >= needed {
		return nil
	}
	const gib = 1 << 30
	return fmt.Errorf(
		"not enough free disk space for WendyOS %s: needs about %.1f GiB in %s, but only %.1f GiB is free.\nFree up space (older downloads in %s can be deleted) and try again",
		plan.version, float64(needed)/gib, cacheDir, float64(avail)/gib, cacheDir)
}

func downloadAndExtractFlashpack(cacheDir string, plan thorFlashPlan, detail func(string)) (*flashpack.Flashpack, bool, error) {
	if !plan.cached {
		// Every manifest entry carries a flashpack checksum; a missing one means a
		// broken (or tampered-with) manifest, so refuse rather than skip verification.
		if plan.info.Checksum == "" {
			return nil, false, fmt.Errorf("manifest entry for %s has no flashpack checksum — refusing to install an unverifiable download", plan.version)
		}
		img := &imageInfo{DownloadURL: plan.info.URL, ImageSize: plan.info.SizeBytes, Version: plan.version}
		tmp, err := downloadImageInto(img, throttledDetail(detail, byteProgress))
		if err != nil {
			return nil, false, fmt.Errorf("downloading flashpack: %w", err)
		}
		detail("verifying")
		if err := verifySHA256(tmp, plan.info.Checksum); err != nil {
			os.Remove(tmp)
			return nil, false, err
		}
		if err := os.Rename(tmp, flashpack.TarballCachePath(cacheDir, plan.version)); err != nil {
			os.Remove(tmp)
			return nil, false, fmt.Errorf("caching flashpack: %w", err)
		}
		// Record provenance next to the (soon-to-be) extracted tree so a later
		// resolve can detect an upstream re-publish under the same version tag.
		// Best-effort: a missing stamp is grandfathered as current, so a write
		// failure forfeits only the staleness check, never the flash.
		_ = flashpack.WriteStamp(filepath.Join(cacheDir, flashpack.FlashpackName(plan.version)), plan.info.Checksum)
	}
	detail("extracting")
	fp, err := flashpack.Resolve(cacheDir, plan.version)
	return fp, plan.cached, err
}

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

// ---- flashing progress UI (cross-platform) ----

type flashStep struct {
	id    int
	label string
	// abortWarning, when set, arms the steps UI's ctrl+c guard while this step
	// runs: the first ctrl+c shows the warning and only a second press within a
	// few seconds actually cancels. Steps without it keep instant cancel.
	abortWarning string
	run          func(out io.Writer, detail func(string)) (cached bool, err error)
}

func runFlashSteps(title string, steps []flashStep, cancelWork func(), logW io.Writer) (int, error) {
	if !isInteractiveTerminal() {
		return runFlashStepsPlain(title, steps, cancelWork, logW)
	}
	m := tui.NewStepsModel(title)
	prog := tui.NewProgressProgram(m)
	type outcome struct {
		failedID int
		err      error
		verbose  []byte
	}
	// curID tracks the step being run so a ctrl+c cancel can report which step
	// was interrupted (the goroutine keeps running briefly after the UI exits).
	var curID atomic.Int32
	curID.Store(-1)
	resC := make(chan outcome, 1)
	go func() {
		failedID, runErr := -1, error(nil)
		var failVerbose []byte
		for _, s := range steps {
			buf := &boundedBuffer{max: maxRawBuildCapture}
			// The step's raw output goes to the bounded in-memory buffer (dumped to
			// stdout on failure) and, unbounded, to the persistent log file.
			out := io.MultiWriter(buf, logW)
			curID.Store(int32(s.id))
			prog.Send(tui.StepAbortGuardMsg{Warning: s.abortWarning})
			prog.Send(tui.StepStartMsg{ID: s.id, Label: s.label})
			cached, err := s.run(out, func(d string) { prog.Send(tui.StepDetailMsg{ID: s.id, Detail: d}) })
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
		// Abort the in-flight step (cancels the stage-2 engine's context) and
		// reap the worker so nothing outlives the CLI. Bounded: a step that
		// ignores cancellation (e.g. stage-1 USB ops) shouldn't wedge the exit.
		cancelWork()
		select {
		case <-resC:
		case <-time.After(10 * time.Second):
			fmt.Fprintln(os.Stderr, "warning: the flash worker didn't stop within 10s; temp files may be left behind")
		}
		return int(curID.Load()), cancelErr
	}
	res := <-resC
	if res.err != nil {
		os.Stdout.Write(res.verbose)
	}
	return res.failedID, res.err
}

func runFlashStepsPlain(title string, steps []flashStep, cancelWork func(), logW io.Writer) (int, error) {
	fmt.Println(title)
	for _, s := range steps {
		fmt.Printf("==> %s\n", s.label)
		out := io.MultiWriter(os.Stdout, logW)
		// A non-interactive flash has no live progress UI, and the multi-minute
		// rootfs write produces no output; a periodic heartbeat keeps CI/SSH
		// idle-output timeouts from killing the process mid-write.
		done := make(chan struct{})
		go stepHeartbeat(out, s.label, done)
		cached, err := runFlashStepPlain(s, out, cancelWork)
		close(done)
		if err != nil {
			return s.id, err
		}
		if cached {
			fmt.Println("    (cached)")
		}
	}
	return -1, nil
}

func runFlashStepPlain(step flashStep, out io.Writer, cancelWork func()) (bool, error) {
	if step.abortWarning == "" {
		return step.run(out, func(string) {})
	}
	type result struct {
		cached bool
		err    error
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	resultC := make(chan result, 1)
	go func() {
		cached, err := step.run(out, func(string) {})
		resultC <- result{cached: cached, err: err}
	}()

	var armedAt time.Time
	const abortWindow = 3 * time.Second
	for {
		select {
		case res := <-resultC:
			return res.cached, res.err
		case <-signals:
			if armedAt.IsZero() || time.Since(armedAt) > abortWindow {
				armedAt = time.Now()
				fmt.Fprintln(os.Stderr, "\n"+step.abortWarning)
				continue
			}
			cancelWork()
			select {
			case <-resultC:
			case <-time.After(10 * time.Second):
				fmt.Fprintln(os.Stderr, "warning: the flash worker didn't stop within 10s; temp files may be left behind")
			}
			return false, tui.ErrCancelled
		}
	}
}

// stepHeartbeat prints a periodic "still in progress" line until done is closed.
func stepHeartbeat(w io.Writer, label string, done <-chan struct{}) {
	start := time.Now()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			fmt.Fprintf(w, "    … %s still in progress (%s elapsed)\n", label, time.Since(start).Round(time.Second))
		}
	}
}

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

// byteProgress formats transfer progress like "40% · 1.2/3.0 GiB". The percent
// is capped at 99 — completion is the step's ✓, and the stage-2 byte count can
// slightly overshoot its estimated total (push retries).
func byteProgress(written, total int64) string {
	const gib = 1 << 30
	if total <= 0 {
		return fmt.Sprintf("%.1f GiB", float64(written)/gib)
	}
	pct := min(written*100/total, 99)
	return fmt.Sprintf("%d%% · %.1f/%.1f GiB", pct, float64(written)/gib, float64(total)/gib)
}

// recoveryWaitHints carries the device-specific text shown while waiting for a
// Jetson to appear in USB recovery mode, so the shared wait UI names the right
// board and cabling/buttons instead of hardcoding Thor.
type recoveryWaitHints struct {
	label       string // e.g. "Thor" or "Jetson AGX Orin"
	cablingLine string // body of the "USB-C cable is in ..." bullet
	buttonLine  string // body of the "recovery button sequence: ..." bullet
}

// thorRecoveryHints is the wait-UI text for a Jetson AGX Thor.
func thorRecoveryHints() recoveryWaitHints {
	return recoveryWaitHints{
		label:       "Thor",
		cablingLine: "the USB-C cable is in the " + briefPort.Render("port next to the HDMI port"),
		buttonLine:  "the recovery button sequence: hold " + briefKey.Render("Force Recovery") + " (middle), tap " + briefKey.Render("Reset") + " (right), release",
	}
}

// waitForRecovery handles an empty recovery scan: the user already confirmed the
// board is in recovery mode, so this usually means cabling or the button
// sequence needs another try. Explain what to check once (device-specific via
// hints), then rescan passively every 1.5s under a spinner until a Jetson
// appears — no keypress needed — or the user quits with q/ctrl+c. Always returns
// ≥1 device on success. Generic so both the gousb (rcm.RecoveryDevice) and
// WinUSB (winusb.Device) scans share it.
func waitForRecovery[T any](hints recoveryWaitHints, scan func() ([]T, error)) ([]T, error) {
	if !isInteractiveTerminal() {
		return nil, fmt.Errorf("no Jetson found in USB recovery mode")
	}

	fmt.Println()
	fmt.Println(tui.WarningMessage("No Jetson in USB recovery mode yet — it will be picked up automatically once it appears."))
	fmt.Println("  While this keeps scanning, double-check:")
	fmt.Println("   • " + hints.cablingLine)
	fmt.Println("   • " + hints.buttonLine)
	fmt.Println()

	p := tui.NewProgressProgram(tui.NewSpinner("Waiting for the " + hints.label + " to appear... (press q to quit)"))
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				devs, err := scan()
				if err != nil {
					p.Send(tui.SpinnerDoneMsg{Err: err})
					return
				}
				if len(devs) > 0 {
					p.Send(tui.SpinnerDoneMsg{Result: devs})
					return
				}
			}
		}
	}()

	finalModel, err := p.Run()
	close(stop)
	if err != nil {
		return nil, fmt.Errorf("recovery scan: %w", err)
	}
	model := finalModel.(tui.SpinnerModel)
	if !model.Done() {
		return nil, ErrUserCancelled // q / ctrl+c
	}
	result, serr := model.Result()
	if serr != nil {
		return nil, serr
	}
	return result.([]T), nil
}

// readQuit reads a line and reports whether the user asked to quit (q/quit).
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

// ---- adb-server stop (used by the macOS/Linux host prep) ----

func stopConflictingADBServer() bool { return stopADBServer("127.0.0.1:5037") }

func stopADBServer(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("0009host:kill")); err != nil {
		return false
	}
	_, _ = io.ReadFull(conn, make([]byte, 4))
	return true
}

// ---- hint boxes (cross-platform) ----

var (
	thorHintBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(tui.ColorError).
			Padding(1, 3)
	thorHintTitle = lipgloss.NewStyle().Foreground(tui.ColorError).Bold(true)
	thorHintEmph  = lipgloss.NewStyle().Foreground(tui.ColorNotice).Bold(true)
	thorHintCmd   = lipgloss.NewStyle().Foreground(tui.Sky500).Bold(true)
)

// printThorBadStateHint warns that an interrupted flash may have left the Thor
// booting only to the UEFI shell, and how to clear the rootfs slot-status marks.
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

// printThorGadgetUnreachableHint is a calm note for a flash that never wrote data.
func printThorGadgetUnreachableHint(w io.Writer) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, tui.WarningMessage("Couldn't reach the Thor's flashing gadget over USB — nothing was written, so the Thor is safe."))
	fmt.Fprintln(w, tui.Dim("  Unplug/replug the USB-C cable (the port next to HDMI), re-enter recovery mode, and flash again."))
}

const (
	usbUdevRulePath = "/etc/udev/rules.d/70-wendy-jetson.rules"
	usbUdevRule     = `SUBSYSTEM=="usb", ATTRS{idVendor}=="0955", MODE="0660", GROUP="plugdev", TAG+="uaccess"`
)

// usbAccessHintBox explains how to regain USB access to the Jetson when the OS
// refuses to open it: on Linux, udev permissions; on macOS, its own kernel driver
// bound to the recovery device (root needed to seize it).
func usbAccessHintBox() string {
	return thorHintBorder.Render(strings.Join(usbAccessHintLines(runtime.GOOS), "\n"))
}

// usbAccessHintLines builds the body of the USB-access-denied hint for the given
// GOOS. Split out from usbAccessHintBox so both OS branches are testable without
// depending on the runner's platform.
func usbAccessHintLines(goos string) []string {
	lines := []string{
		thorHintTitle.Render("⚠  USB access denied"),
		"",
		"A Jetson is in recovery mode, but the OS refused wendy access to its USB device.",
	}
	if goos == "linux" {
		return append(lines,
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
	}
	// macOS: the device enumerated but couldn't be claimed. macOS binds its own
	// driver to the recovery device (often re-matched after a failed RCM boot), so
	// wendy needs root to seize it — lead with the two fixes that actually work.
	return append(lines,
		"It enumerated but couldn't be claimed: macOS binds its own driver to the",
		"recovery device (often re-matched after a failed RCM boot), so wendy needs",
		"root to seize it. Try, in order:",
		"",
		"  "+thorHintEmph.Render("•")+" Re-run the flash with "+thorHintCmd.Render("sudo")+".",
		"  "+thorHintEmph.Render("•")+" Or unplug the USB-C cable, re-enter recovery mode, and flash again.",
		"",
		"If another process holds it (a VM with USB passthrough, another flashing",
		"tool, or another wendy), quit that first.",
	)
}
