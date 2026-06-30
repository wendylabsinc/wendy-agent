//go:build darwin

package commands

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/bringup"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/flasher"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/flashpack"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/rcm"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/shim"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// installThor flashes a Jetson AGX Thor (T264) over USB recovery: resolve the
// flashpack (cache-first, else download from the manifest), pick the device, then
// stage-1 RCM boot → stage-2 ADB partition flash. macOS only.
func installThor(ctx context.Context, version string, nightly, force bool) error {
	cacheDir, err := osCacheDir()
	if err != nil {
		return fmt.Errorf("resolving cache dir: %w", err)
	}

	fp, err := resolveThorFlashpack(cacheDir, version, nightly)
	if err != nil {
		return err
	}
	fmt.Printf("Flashpack: %s (WendyOS %s)\n", fp.Root, fp.Manifest.WendyOSVersion)

	// Brief the user on cabling / recovery mode, then confirm before scanning.
	if err := confirmThorReady(); err != nil {
		return err
	}

	// Pick the device to flash.
	dev, err := pickRecoveryDevice()
	if err != nil {
		return err
	}
	fmt.Printf("Target: %s\n", dev.Describe())

	if !force {
		fmt.Printf("\nThis ERASES and reflashes the Thor (QSPI + internal NVMe). This cannot be undone.\nFlash %s? [y/N] ", dev.Describe())
		if !readYes() {
			return ErrUserCancelled
		}
	}

	// Materialize wendy's own adb/lsusb/timeout shim for bootburn, and pin the
	// flashing gadget to the selected device's USB location for stage 2.
	shimDir, err := shim.MaterializeADBDir()
	if err != nil {
		return fmt.Errorf("preparing adb shim: %w", err)
	}
	defer os.RemoveAll(shimDir)
	// bootburn also calls adb by absolute path inside the bundle and workspace.
	for _, p := range []string{
		filepath.Join(fp.BundleDir(), "unified_flash", "tools", "flashtools", "flash", "adb"),
		filepath.Join(fp.WorkspaceOutDir(), "tools", "flashtools", "flash", "adb"),
	} {
		if err := shim.LinkSelfAt(p); err != nil {
			return fmt.Errorf("installing adb shim at %s: %w", p, err)
		}
	}
	fmt.Println("\n== Stage 1: RCM boot ==")
	if err := bringup.Run(bringup.Options{
		Dir:        fp.Stage1Dir(),
		MemBCT:     fp.MemBCT(),
		DevicePath: dev.PathKey,
		SendOrder:  fp.Manifest.Stage1SendOrder,
		Out:        os.Stdout,
	}); err != nil {
		return fmt.Errorf("stage 1 (RCM boot): %w", err)
	}

	// Log bootburn's verbose output to a conventional logs dir, not the terminal.
	logPath := ""
	if dir, derr := config.LogDir(); derr == nil {
		logPath = filepath.Join(dir, "thor-flash-"+time.Now().Format("20060102-150405")+".log")
	}

	fmt.Println("\n== Stage 2: flash partitions over ADB ==")
	if err := flasher.Run(flasher.Options{
		BundleDir:    fp.BundleDir(),
		WorkspaceDir: fp.WorkspaceOutDir(),
		ADBDir:       shimDir,
		ADBPort:      dev.PathKey, // pin the flashing gadget to the selected device
		LogPath:      logPath,
		PyYAMLDir:    fp.PyYAMLDir(),
		Out:          os.Stdout,
	}); err != nil {
		return fmt.Errorf("stage 2 (flash): %w", err)
	}

	fmt.Println("\nFlash complete. Power-cycle the Thor out of recovery to boot WendyOS.")
	return nil
}

// resolveThorFlashpack returns the flashpack for version, cache-first, downloading
// from the manifest on a cache miss. An empty version means the manifest's latest.
func resolveThorFlashpack(cacheDir, version string, nightly bool) (*flashpack.Flashpack, error) {
	if version != "" {
		if fp, err := flashpack.Resolve(cacheDir, version); err == nil {
			return fp, nil
		} else if !errors.Is(err, flashpack.ErrNotInCache) {
			return nil, err
		}
	}

	// Cache miss (or no version given): consult the manifest.
	info, mErr := getThorFlashpackInfo(version, nightly)
	if mErr != nil {
		if version != "" {
			return nil, fmt.Errorf("flashpack %s not in cache and manifest lookup failed: %w", version, mErr)
		}
		return nil, mErr
	}
	version = info.Version

	if fp, err := flashpack.Resolve(cacheDir, version); err == nil {
		return fp, nil
	} else if !errors.Is(err, flashpack.ErrNotInCache) {
		return nil, err
	}

	fmt.Printf("Downloading Thor flashpack %s...\n", version)
	tmp, err := downloadImage(&imageInfo{DownloadURL: info.URL, ImageSize: info.SizeBytes, Version: version})
	if err != nil {
		return nil, fmt.Errorf("downloading flashpack: %w", err)
	}
	// Verify the download against the manifest checksum before trusting it.
	if info.Checksum != "" {
		if err := verifySHA256(tmp, info.Checksum); err != nil {
			os.Remove(tmp)
			return nil, err
		}
	}
	dest := flashpack.TarballCachePath(cacheDir, version)
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("caching flashpack: %w", err)
	}
	return flashpack.Resolve(cacheDir, version)
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
		if err != nil {
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

// thorRecoveryBriefing explains the cabling and recovery-mode steps the user
// must complete before a Thor will appear in the USB recovery scan.
const thorRecoveryBriefing = `
Before flashing, please read the following:

  Storage
    WendyOS is installed to the Thor's internal flash and NVMe drive. It does
    not use an external USB drive — if one is connected, disconnect it now and
    leave it out for the rest of this process.

  USB-C cabling
    Connect this computer to the Thor's USB-C port directly next to the HDMI
    port. The other USB-C port may be used for power only.

  Entering recovery mode
    The three front buttons, left to right, are Power, Force Recovery, and Reset.
    1. Start with the Thor unplugged and powered off.
    2. Plug in power.
    3. Press and hold the Force Recovery button (middle). While holding it,
       briefly press the Reset button (right), then release Force Recovery.
    4. Connect it to this computer using the USB-C port next to the HDMI port.
`

// confirmThorReady prints the recovery-mode briefing and asks the user to
// confirm the target Thor is connected and in recovery mode before scanning.
// Returns ErrUserCancelled if the user declines.
func confirmThorReady() error {
	fmt.Print(thorRecoveryBriefing)
	fmt.Print("\nIs the target Thor connected and in recovery mode? [y/n] ")
	if !readYes() {
		return ErrUserCancelled
	}
	return nil
}

// readYes reads a single line and reports whether it is an affirmative (y/yes).
func readYes() bool {
	s := bufio.NewScanner(os.Stdin)
	if !s.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(s.Text())) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
