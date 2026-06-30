//go:build darwin || linux || windows

package commands

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"

	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	"github.com/wendylabsinc/wendy/go/internal/shared/wendyconf"
)

type preEnrollMode int

const (
	preEnrollAuto   preEnrollMode = iota // prompt if interactive terminal + auth session exists
	preEnrollForced                      // --pre-enroll explicitly set to true
	preEnrollSkip                        // --pre-enroll explicitly set to false
)

func newOSInstallCmd() *cobra.Command {
	var nightly bool
	var force bool
	var noBmap bool
	var storageOverride string
	var yesOverwriteInternal bool
	var preEnroll bool
	var deviceType string
	var versionFlag string
	var driveFlag string
	var wifiSSID string
	var wifiPassword string
	var wifiEntries []string
	var noWifi bool
	var deviceName string
	var enrollCloudGRPC string

	cmd := &cobra.Command{
		Use:   "install [image] [drive]",
		Short: "Install WendyOS or Wendy Lite firmware on a device",
		Long: `Interactively select a supported device, download the latest OS image or firmware, and write it to the target.

When called with positional arguments, skips interactive prompts:
  wendy os install <image-path> <drive-id> --force

When called with manifest-backed flags, installs a specific version:
  wendy os install --device-type raspberry-pi-5 --version 0.10.4 --drive /dev/disk4 --force

Pre-seed multiple WiFi networks (repeatable, highest-priority first):
  wendy os install --device-type raspberry-pi-5 --drive /dev/disk4 --force \
    --wifi "ssid=Home,password=hunter2,priority=100" \
    --wifi "ssid=Office,password=corp,priority=50" \
    --wifi "ssid=Cafe,hidden=true"

Flags can be provided progressively — omitted values trigger interactive pickers.`,
		Args: func(cmd *cobra.Command, args []string) error {
			switch len(args) {
			case 0, 2:
				return nil
			case 1:
				return fmt.Errorf("positional arguments must be provided as [image] [drive]; got 1 argument")
			default:
				return cobra.MaximumNArgs(2)(cmd, args)
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Positional direct-install mode is incompatible with manifest-backed flags.
			if len(args) > 0 && (deviceType != "" || versionFlag != "" || driveFlag != "" || wifiSSID != "" || wifiPassword != "" || len(wifiEntries) > 0 || noWifi || deviceName != "" || enrollCloudGRPC != "") {
				return fmt.Errorf("positional [image] [drive] arguments cannot be combined with --device-type, --version, --drive, --wifi-ssid, --wifi-password, --wifi, --no-wifi, --device-name, or --cloud-grpc")
			}
			if nightly && versionFlag != "" {
				return fmt.Errorf("--nightly and --version are mutually exclusive")
			}

			opts := wifiCLIOptions{
				SSID:     wifiSSID,
				Password: wifiPassword,
				Entries:  wifiEntries,
				NoWifi:   noWifi,
			}

			if len(args) == 2 {
				return runOSInstallDirect(args[0], args[1], force, yesOverwriteInternal)
			}
			mode := preEnrollAuto
			if cmd.Flags().Changed("pre-enroll") {
				if preEnroll {
					mode = preEnrollForced
				} else {
					mode = preEnrollSkip
				}
			}
			return runOSInstall(cmd.Context(), nightly, deviceType, versionFlag, driveFlag, force, yesOverwriteInternal, noBmap, storageOverride, opts, deviceName, preEnrollOptions{mode: mode, cloudGRPC: enrollCloudGRPC})
		},
	}

	cmd.Flags().BoolVar(&nightly, "nightly", false, "Use nightly/prerelease builds")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&noBmap, "no-bmap", false, "Disable bmap-accelerated flashing even when a block map is available")
	cmd.Flags().StringVar(&storageOverride, "storage", "", "Force image storage variant: nvme or sd (default: auto-detect — real NVMe drives use nvme; a USB-attached drive uses the device's published image, SD for Raspberry Pi / NVMe for Jetson SSD enclosures)")
	cmd.Flags().BoolVar(&yesOverwriteInternal, "yes-overwrite-internal", false, "Required to wipe an internal (non-removable) drive in non-interactive mode")
	cmd.Flags().StringVar(&deviceType, "device-type", "", "Device type from manifest (e.g. raspberry-pi-5)")
	cmd.Flags().StringVar(&versionFlag, "version", "", "WendyOS version to install (interactive if omitted)")
	cmd.Flags().StringVar(&driveFlag, "drive", "", "Target drive path (e.g. /dev/disk4)")
	cmd.Flags().StringVar(&wifiSSID, "wifi-ssid", "", "Pre-configure a single WiFi SSID on first boot (shortcut for --wifi)")
	cmd.Flags().StringVar(&wifiPassword, "wifi-password", "", "Password for --wifi-ssid")
	cmd.Flags().StringArrayVar(&wifiEntries, "wifi", nil, "Pre-configure a WiFi network. Repeatable. Format: ssid=X[,password=Y][,priority=N][,hidden=true][,security=wpa2]")
	cmd.Flags().BoolVar(&noWifi, "no-wifi", false, "Skip WiFi setup entirely (no interactive prompt, no pre-seeded networks)")
	cmd.Flags().StringVar(&deviceName, "device-name", "", "Set device name on first boot (e.g. brave-dolphin)")
	cmd.Flags().BoolVar(&preEnroll, "pre-enroll", false, "Pre-enroll this device with Wendy Cloud during imaging (requires 'wendy auth login')")
	cmd.Flags().StringVar(&enrollCloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint of the auth session to use for pre-enrollment (optional when a default is set via 'wendy auth use')")

	return cmd
}

func runOSInstallDirect(imagePath string, driveID string, force bool, yesOverwriteInternal bool) error {
	// Verify the image file exists.
	if _, err := os.Stat(imagePath); err != nil {
		return fmt.Errorf("image file: %w", err)
	}

	// Authenticate elevation before any disk-listing or write work. On
	// Windows this offers a UAC re-launch when the current process isn't
	// elevated; on Unix it pre-caches the sudo timestamp.
	if err := preAuthElevation(); err != nil {
		return err
	}

	// Find the target drive.
	drives, err := listAllDrives()
	if err != nil {
		return fmt.Errorf("listing drives: %w", err)
	}

	var targetDrive *drive
	for _, d := range drives {
		if d.DevicePath == driveID {
			targetDrive = &d
			break
		}
	}
	if targetDrive == nil {
		return fmt.Errorf("drive %s not found", driveID)
	}

	if err := confirmOverwriteInternalDrive(*targetDrive, force, yesOverwriteInternal); err != nil {
		return err
	}

	if !force {
		confirmed, err := tui.Confirm(fmt.Sprintf("Writing will ERASE ALL DATA on %s (%s). Continue?", targetDrive.Name, targetDrive.DevicePath))
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	stream, err := openLocalImageStream(imagePath)
	if err != nil {
		return fmt.Errorf("opening image: %w", err)
	}
	defer stream.Close()

	fmt.Printf("Writing image to %s...\n", targetDrive.DevicePath)
	fmt.Println(elevationHint())
	if err := writeImageToDisk(stream, stream.uncompressedSize, *targetDrive, nil); err != nil {
		return fmt.Errorf("writing image: %w", err)
	}

	fmt.Printf("\nSuccessfully installed image on %s.\n", targetDrive.Name)
	return nil
}

// pickerDevice is a unified entry for the device selection picker.
type pickerDevice struct {
	Name       string
	Version    string // display version (e.g. "0.10.5 (nightly)")
	RawVersion string // exact version key for manifest lookup
	IsESP32    bool
	ESP32Chip  string          // e.g. "esp32c6", "esp32c5"
	Manifest   *deviceManifest // cached manifest for Linux devices
}

// pickLinuxDevice fetches available Linux devices from the manifest and presents
// an interactive picker. Returns the selected device key and its deviceInfo.
func pickLinuxDevice() (string, deviceInfo, error) {
	fmt.Println("Fetching available devices...")

	devices, err := getAvailableDevices()
	if err != nil {
		log.Printf("WARNING: could not fetch Linux device manifest: %v", err)
	}

	var items []tui.PickerItem
	deviceMap := make(map[string]deviceInfo)

	for _, dev := range devices {
		if dev.LatestVersion == "" {
			continue
		}
		deviceMap[dev.Key] = dev
		items = append(items, tui.PickerItem{
			Name:        dev.Name,
			Description: fmt.Sprintf("(latest: %s)", dev.LatestVersion),
			Value:       dev.Key,
		})
	}

	if len(items) == 0 {
		return "", deviceInfo{}, fmt.Errorf("no devices available")
	}

	fmt.Println()
	key, err := pickFromItems("Select a device", items)
	if err != nil {
		return "", deviceInfo{}, err
	}
	return key, deviceMap[key], nil
}

func runOSInstall(ctx context.Context, nightly bool, flagDeviceType, flagVersion, flagDrive string, force bool, yesOverwriteInternal bool, noBmap bool, storageOverride string, wifi wifiCLIOptions, deviceName string, preOpts preEnrollOptions) error {
	if storageOverride != "" && storageOverride != "nvme" && storageOverride != "sd" {
		return fmt.Errorf("invalid --storage %q: must be \"nvme\" or \"sd\"", storageOverride)
	}
	fmt.Println("Fetching available devices...")

	// Fetch Linux devices from GCS manifest.
	linuxDevices, err := getAvailableDevices()
	if err != nil {
		log.Printf("WARNING: could not fetch Linux device manifest: %v", err)
	}

	// Build picker items.
	var items []tui.PickerItem
	deviceMap := make(map[string]pickerDevice)

	for _, dev := range linuxDevices {
		rawVersion := dev.LatestVersion
		displayVersion := "(" + rawVersion + ")"
		if nightly && dev.NightlyVersion != "" {
			rawVersion = dev.NightlyVersion
			displayVersion = "(" + rawVersion + ", nightly)"
		}
		if rawVersion == "" {
			continue // skip devices with no available version
		}

		pd := pickerDevice{
			Name:       dev.Name,
			Version:    displayVersion,
			RawVersion: rawVersion,
			Manifest:   dev.Manifest,
		}
		deviceMap[dev.Key] = pd

		items = append(items, tui.PickerItem{
			Name:        dev.Name,
			Description: displayVersion,
			Section:     "WendyOS",
			SortKey:     "0_wendyos_" + strings.ToLower(dev.Name),
			Value:       dev.Key,
		})
	}

	// When --device-type is not provided, also offer ESP32 entries in the picker.
	if flagDeviceType == "" {
		espVersion := "(latest)"
		for _, esp := range []struct {
			key, name, chip string
		}{
			{"esp32-c6", "ESP32-C6", "esp32c6"},
			{"esp32-c5", "ESP32-C5", "esp32c5"},
		} {
			deviceMap[esp.key] = pickerDevice{
				Name:      esp.name,
				Version:   espVersion,
				IsESP32:   true,
				ESP32Chip: esp.chip,
			}
			items = append(items, tui.PickerItem{
				Name:        esp.name,
				Description: espVersion,
				Section:     "Wendy Lite",
				SortKey:     "1_lite_" + strings.ToLower(esp.name),
				Value:       esp.key,
			})
		}
	}

	// Resolve device — use flag or interactive picker.
	var selected string
	if flagDeviceType != "" {
		// --device-type is only supported for Linux devices, not ESP32/Wendy Lite.
		if flagDeviceType == "esp32-c6" || flagDeviceType == "esp32-c5" {
			return fmt.Errorf("--device-type does not support ESP32 targets; use the interactive picker for Wendy Lite devices")
		}
		if _, ok := deviceMap[flagDeviceType]; !ok {
			var available []string
			for k, d := range deviceMap {
				if !d.IsESP32 {
					available = append(available, k)
				}
			}
			sort.Strings(available)
			return fmt.Errorf("device type %q not found in manifest; available: %s", flagDeviceType, strings.Join(available, ", "))
		}
		selected = flagDeviceType
	} else {
		if len(items) == 0 {
			return fmt.Errorf("no devices available")
		}

		fmt.Println()
		selected, err = pickFromItems("Select a device", items)
		if err != nil {
			return err
		}
	}

	device := deviceMap[selected]

	if device.IsESP32 {
		return installESP32Firmware(ctx, nightly, device.ESP32Chip, wifi, deviceName, preOpts)
	}
	return installLinuxImage(ctx, selected, device, nightly, flagVersion, flagDrive, force, yesOverwriteInternal, noBmap, storageOverride, wifi, deviceName, preOpts)
}

// installLinuxImage handles the Linux device path: pick version → pick drive → download → write.
// nightly, flagVersion, flagDrive, and force allow skipping the corresponding interactive prompts.
func installLinuxImage(ctx context.Context, deviceKey string, device pickerDevice, nightly bool, flagVersion, flagDrive string, force bool, yesOverwriteInternal bool, noBmap bool, storageOverride string, wifi wifiCLIOptions, deviceName string, preOpts preEnrollOptions) error {
	// Authenticate elevation up front so we don't pay for the multi-hundred-MB
	// image download just to discover the user can't write to a raw disk. On
	// Windows this offers a UAC re-launch when not elevated; on Unix it
	// pre-caches the sudo timestamp.
	if err := preAuthElevation(); err != nil {
		return err
	}
	// The download can take minutes or hours, which is longer than the default
	// sudo credential cache window. Refresh in the background so the cached
	// timestamp never expires before writeImageToDisk runs.
	elevationCtx, cancelElevation := context.WithCancel(ctx)
	defer cancelElevation()
	keepElevationAlive(elevationCtx)

	// Step 1: Resolve version — use flag, nightly shortcut, or pick interactively.
	selectedVersion := device.RawVersion // default: latest (or nightly if --nightly)
	if flagVersion != "" {
		// Validate the requested version exists in the manifest.
		if _, err := getImageInfo(device.Manifest, flagVersion, ""); err != nil {
			return fmt.Errorf("version %q not found for %s", flagVersion, device.Name)
		}
		selectedVersion = flagVersion
	}

	// Step 2: Resolve target drive — use flag or interactive picker.
	var targetDrive drive
	if flagDrive != "" {
		drives, err := listAllDrives()
		if err != nil {
			return fmt.Errorf("listing drives: %w", err)
		}
		var found bool
		for _, d := range drives {
			if d.DevicePath == flagDrive {
				targetDrive = d
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("drive %s not found", flagDrive)
		}
	} else {
		fmt.Println()
		selectedDrive, err := pickExternalDrive(ctx)
		if err != nil {
			return err
		}
		targetDrive = selectedDrive
	}

	// Step 3: Confirm destructive write (unless --force).
	if err := confirmOverwriteInternalDrive(targetDrive, force, yesOverwriteInternal); err != nil {
		return err
	}
	if !force {
		fmt.Println()
		confirmed, err := tui.Confirm(fmt.Sprintf("Writing will ERASE ALL DATA on %s (%s). Continue?", targetDrive.Name, targetDrive.DevicePath))
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	provCreds, err := resolveWiFiCredentialsList(wifi)
	if err != nil {
		return err
	}

	provDeviceName, err := resolveDeviceName(deviceName)
	if err != nil {
		return err
	}

	// Resolve pre-enrollment — must happen before provisionConfigPartition because
	// the config partition is mounted and unmounted inside that call.
	var provisioningJSON []byte
	if preOpts.mode != preEnrollSkip {
		cfg, cfgErr := config.Load()
		if cfgErr != nil {
			if preOpts.mode == preEnrollForced {
				return fmt.Errorf("--pre-enroll: loading config: %w", cfgErr)
			}
			cfg = &config.Config{} // auto mode: treat an unreadable config as not logged in
		}
		provisioning, resolveErr := resolvePreEnrollment(ctx, cfg, preOpts, isInteractiveTerminal(), provDeviceName)
		if resolveErr != nil {
			return resolveErr
		}
		if provisioning != nil {
			provisioningJSON, err = json.Marshal(provisioning)
			if err != nil {
				return fmt.Errorf("marshaling provisioning state: %w", err)
			}
		}
	}

	// Step 5: Resolve image metadata for the target storage. A USB-attached
	// drive is ambiguous (SD card in a reader vs NVMe SSD in an enclosure), so
	// the variant is chosen from what this manifest version publishes — see
	// manifestStorage. Pass --storage to override.
	ver, ok := device.Manifest.Versions[selectedVersion]
	if !ok {
		return fmt.Errorf("version %s not found in device manifest", selectedVersion)
	}
	storage := manifestStorage(ver, targetDrive.StorageType, targetDrive.MediaFixed, storageOverride)

	// A USB-attached SD reader and an SSD enclosure are indistinguishable by bus
	// type, and not every platform reports fixed-media signals. When the variant
	// is a guess on a dual-variant device, ask the user which slot the drive is
	// for rather than silently picking the wrong image (a wrong guess can produce
	// a non-booting device). Non-interactive runs keep the manifest default.
	if storageOverride == "" && storageChoiceAmbiguous(ver, targetDrive.StorageType, targetDrive.MediaFixed) && isInteractiveTerminal() {
		bootNVMe, err := tui.ConfirmNoDefault(fmt.Sprintf(
			"Can't tell if %s is an SD card or an SSD. Will it boot from the NVMe/SSD slot?",
			targetDrive.DevicePath))
		if err != nil {
			return err
		}
		if bootNVMe {
			storage = "nvme"
		} else {
			storage = "sd"
		}
	} else if storageOverride == "" && targetDrive.MediaFixed && storage == "nvme" {
		// Auto-detected an SSD enclosure. Surface the inference and the override
		// so a surprising choice is at least visible and reversible.
		fmt.Printf("Detected a fixed SSD on %s — using the NVMe image (pass --storage sd to override).\n", targetDrive.DevicePath)
	}

	fmt.Printf("\nPreparing %s %s image...\n", device.Name, selectedVersion)
	imgInfo, err := getImageInfo(device.Manifest, selectedVersion, storage)
	if err != nil {
		return fmt.Errorf("getting image info: %w", err)
	}

	// Step 5a: Prefer the seekable-zstd fast path. When the manifest advertises a
	// .zst for this storage plus a usable bmap (and --no-bmap wasn't passed), we
	// download only the .zst + bmap and write mapped ranges, skipping holes —
	// and crucially we do NOT download the full .zip image at all.
	var seekableZst, seekableBmap string
	var seekableTotal int64
	if !noBmap && imgInfo.ZstURL != "" && imgInfo.BmapURL != "" {
		zstPath, zerr := resolveSeekableZst(deviceKey, selectedVersion, storage, imgInfo.ZstURL)
		bmapCandidate, berr := osCachedBmapPath(deviceKey, selectedVersion, storage)
		switch {
		case zerr != nil:
			fmt.Printf("Note: could not fetch seekable image (%v); flashing the full image.\n", zerr)
		case berr != nil:
			fmt.Printf("Note: cannot resolve block-map cache path (%v); flashing the full image.\n", berr)
		default:
			if derr := downloadBmap(imgInfo.BmapURL, bmapCandidate); derr != nil {
				fmt.Printf("Note: could not fetch block map (%v); flashing the full image.\n", derr)
			} else if parsed, perr := parseBmap(readFileOrNil(bmapCandidate)); perr != nil {
				fmt.Printf("Note: block map unusable (%v); flashing the full image.\n", perr)
			} else {
				seekableZst, seekableBmap, seekableTotal = zstPath, bmapCandidate, mappedBytes(parsed)
			}
		}
	}

	// Step 5b: Fallback path — resolve the .zip/.img stream only when NOT using
	// the seekable path (so the seekable path never downloads the .zip). For
	// compressed images, measure the size (skipped when a bmap is present, since
	// the bmap's ImageSize is the exact total) and prepare the legacy block map.
	var stream *imageStream
	var bmapPath string
	if seekableZst == "" {
		stream, err = openOSImageStream(deviceKey, imgInfo)
		if err != nil {
			return fmt.Errorf("opening OS image: %w", err)
		}
		defer stream.Close()

		if stream.uncompressedSize == 0 && stream.sourcePath != "" && imgInfo.BmapURL == "" {
			if err := measureImageWithProgress(stream); err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				fmt.Printf("Could not determine image size: %v\n", err)
			}
		}

		if !noBmap && imgInfo.BmapURL != "" {
			candidate, derr := osCachedBmapPath(deviceKey, selectedVersion, storage)
			if derr != nil {
				fmt.Printf("Note: cannot resolve block-map cache path (%v); flashing the full image.\n", derr)
			} else if derr := downloadBmap(imgInfo.BmapURL, candidate); derr != nil {
				fmt.Printf("Note: could not fetch block map (%v); flashing the full image.\n", derr)
			} else if parsed, perr := parseBmap(readFileOrNil(candidate)); perr != nil {
				fmt.Printf("Note: block map unusable (%v); flashing the full image.\n", perr)
			} else if stream.uncompressedSize > 0 && parsed.ImageSize != stream.uncompressedSize {
				fmt.Printf("Note: block map is for a %d-byte image but this image is %d bytes; flashing the full image.\n", parsed.ImageSize, stream.uncompressedSize)
			} else {
				bmapPath = candidate
			}
		}
	}

	// Step 6: Write image to drive with progress bar.
	fmt.Printf("Writing image to %s...\n", targetDrive.DevicePath)
	writeProg := tui.NewProgress(fmt.Sprintf("Writing to %s...", targetDrive.DevicePath))
	if seekableZst != "" || bmapPath != "" {
		// bmap failures fall back silently; suppress the TUI error render
		writeProg = writeProg.WithoutErrorView()
	}
	wp := tui.NewProgressProgram(writeProg)

	go func() {
		var writeErr error
		switch {
		case seekableZst != "":
			fmt.Println("Using seekable block map for faster flashing.")
			writeErr = writeImageWithBmapSeekable(seekableZst, seekableBmap, targetDrive, func(written int64) {
				var pct float64
				if seekableTotal > 0 {
					pct = float64(written) / float64(seekableTotal)
				}
				wp.Send(tui.ProgressUpdateMsg{
					Percent: pct,
					Written: written,
					Total:   seekableTotal,
				})
			})
		case bmapPath != "":
			fmt.Println("Using block map for faster flashing.")
			writeErr = writeImageWithBmap(stream, stream.uncompressedSize, targetDrive, bmapPath, func(written int64) {
				if msg, ok := stream.writeProgressMsg(written); ok {
					wp.Send(msg)
				}
			})
		default:
			writeErr = writeImageToDisk(stream, stream.uncompressedSize, targetDrive, func(written int64) {
				if msg, ok := stream.writeProgressMsg(written); ok {
					wp.Send(msg)
				}
			})
		}
		wp.Send(tui.ProgressDoneMsg{Err: writeErr})
	}()

	writeFinal, err := wp.Run()
	if err != nil {
		return fmt.Errorf("progress TUI: %w", err)
	}

	writeModel := writeFinal.(tui.ProgressModel)
	if writeModel.Err() != nil {
		// Bmap write failed (typically a checksum mismatch between the published
		// bmap and the actual image). Fall back to a full sequential dd write.
		// For the seekable-zstd path the .zst is already on disk — open it as a
		// sequential reader to avoid re-downloading the full image. For the regular
		// bmap path the zip is also already cached.
		var fallbackReader io.Reader
		var fallbackSize int64
		var fallbackCloser io.Closer
		switch {
		case seekableZst != "":
			si, ferr := openSeekableZstd(seekableZst)
			if ferr != nil {
				return fmt.Errorf("writing image: %w", writeModel.Err())
			}
			fallbackReader, fallbackSize, fallbackCloser = si, si.Size(), si
		case bmapPath != "":
			fs, ferr := openOSImageStream(deviceKey, imgInfo)
			if ferr != nil {
				return fmt.Errorf("writing image: %w", writeModel.Err())
			}
			fallbackReader, fallbackSize, fallbackCloser = fs, fs.uncompressedSize, fs
		default:
			return fmt.Errorf("writing image: %w", writeModel.Err())
		}
		defer fallbackCloser.Close()
		fallbackProg := tui.NewProgress(fmt.Sprintf("Writing to %s...", targetDrive.DevicePath))
		fp := tui.NewProgressProgram(fallbackProg)
		go func() {
			fp.Send(tui.ProgressDoneMsg{Err: writeImageToDisk(fallbackReader, fallbackSize, targetDrive, func(written int64) {
				if fallbackSize > 0 {
					fp.Send(tui.ProgressUpdateMsg{
						Percent: float64(written) / float64(fallbackSize),
						Written: written,
						Total:   fallbackSize,
					})
				}
			})})
		}()
		fallbackFinal, ferr := fp.Run()
		if ferr != nil {
			return fmt.Errorf("progress TUI: %w", ferr)
		}
		if fm := fallbackFinal.(tui.ProgressModel); fm.Err() != nil {
			return fmt.Errorf("writing image: %w", fm.Err())
		}
	}

	hasProvisioningData := provisioningRequired(provCreds, provDeviceName, provisioningJSON)

	if !configPartitionSupported {
		// writeConfigPartition is not supported on this platform. Skip the
		// agent download — paying 5–30s of network for a guaranteed-skipped
		// step is the bug from WDY-1118.
		if hasProvisioningData {
			ejectDisk(targetDrive)
			return fmt.Errorf("the OS image was written to %s, but --wifi, --device-name, and --pre-enroll cannot be applied on this platform: writing to the device's config partition is not supported here. Re-run on a platform that supports config-partition provisioning to apply provisioning, or omit those flags to image without provisioning", targetDrive.Name)
		}
		fmt.Println("\nNote: config-partition provisioning is not yet supported on this platform; skipping. The device will run the agent baked into the image and fetch updates after first boot.")
	} else {
		fmt.Printf("\nWriting provisioning data to config partition...\n")
		// A provisioning failure is not a flash failure: the OS image already
		// landed on the drive above. provisionConfigWithRetry warns and offers
		// an interactive retry rather than aborting, so the user knows the
		// device still boots.
		provisionConfigWithRetry(targetDrive, provCreds, provDeviceName, provisioningJSON, hasProvisioningData)
	}

	ejectDisk(targetDrive)

	fmt.Printf("\nSuccessfully installed %s %s on %s.\n", device.Name, imgInfo.Version, targetDrive.Name)
	fmt.Println("You can now insert the drive into your device and power it on.")
	return nil
}

// pickManifestVersion presents an interactive picker for available versions in a
// device manifest, sorted newest-first using semantic version comparison. It
// marks "latest" and "nightly" versions in the picker description.
// This is shared by both os install and os download flows.
func pickManifestVersion(title string, manifest *deviceManifest) (string, error) {
	if manifest == nil || len(manifest.Versions) == 0 {
		return "", fmt.Errorf("no versions available")
	}

	var versionKeys []string
	for v := range manifest.Versions {
		versionKeys = append(versionKeys, v)
	}
	sort.Slice(versionKeys, func(i, j int) bool {
		return version.CompareVersions(versionKeys[i], versionKeys[j]) > 0
	})

	var items []tui.PickerItem
	for _, v := range versionKeys {
		ver := manifest.Versions[v]
		desc := ""
		if ver.IsLatest {
			desc = "latest"
		} else if ver.IsNightly {
			desc = "nightly"
		}
		items = append(items, tui.PickerItem{
			Name:        v,
			Description: desc,
			Value:       v,
		})
	}

	fmt.Println()
	return pickFromItems(title, items)
}

const externalDrivePickerRefreshInterval = 2 * time.Second

func pickExternalDrive(ctx context.Context) (drive, error) {
	item, err := pickRefreshingItem(ctx, "Select target drive", externalDrivePickerRefreshInterval, func(context.Context) ([]tui.PickerItem, error) {
		drives, err := listExternalDrives()
		if err != nil {
			return nil, fmt.Errorf("listing drives: %w", err)
		}
		return externalDrivePickerItems(drives), nil
	})
	if err != nil {
		return drive{}, err
	}
	selected, ok := item.Value.(drive)
	if !ok {
		return drive{}, fmt.Errorf("invalid drive selection")
	}
	return selected, nil
}

func externalDrivePickerItems(drives []drive) []tui.PickerItem {
	items := make([]tui.PickerItem, 0, len(drives))
	for _, d := range drives {
		desc := d.DevicePath
		if d.Size != "" {
			desc += "  " + d.Size
		}
		items = append(items, tui.PickerItem{
			Name:        d.Name,
			Description: desc,
			DedupKey:    d.DevicePath,
			Value:       d,
		})
	}
	return items
}

func throttledProgress(p *tea.Program, minInterval time.Duration) func(written, total int64) {
	var lastNanos atomic.Int64
	return func(written, total int64) {
		if total <= 0 {
			return
		}
		now := time.Now()
		prev := lastNanos.Load()
		if now.UnixNano()-prev < minInterval.Nanoseconds() {
			return
		}
		if !lastNanos.CompareAndSwap(prev, now.UnixNano()) {
			return
		}
		p.Send(tui.ProgressUpdateMsg{
			Percent: float64(written) / float64(total),
			Written: written,
			Total:   total,
		})
	}
}

const parallelDownloadWorkers = 8

// downloadChunk fetches the byte range [start, end] from url, writes it to dst
// at the correct offset via WriteAt, and atomically increments *downloaded.
func downloadChunk(client *http.Client, url string, start, end int64, dst *os.File, downloaded *int64, total int64, sendProgress func(int64, int64)) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("range request %d-%d: %w", start, end, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("range request %d-%d: expected 206, got %d", start, end, resp.StatusCode)
	}

	buf := make([]byte, 1*1024*1024)
	offset := start
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := dst.WriteAt(buf[:n], offset); writeErr != nil {
				return fmt.Errorf("writing at offset %d: %w", offset, writeErr)
			}
			offset += int64(n)
			newTotal := atomic.AddInt64(downloaded, int64(n))
			sendProgress(newTotal, total)
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("reading chunk %d-%d: %w", start, end, readErr)
		}
	}
}

// downloadParallel downloads url into dst using parallelDownloadWorkers concurrent
// range requests. dst must already be truncated to contentLength bytes.
func downloadParallel(client *http.Client, url string, contentLength int64, dst *os.File, sendProgress func(int64, int64)) error {
	chunkSize := (contentLength + parallelDownloadWorkers - 1) / parallelDownloadWorkers

	var wg sync.WaitGroup
	errCh := make(chan error, parallelDownloadWorkers)
	var downloaded int64

	for i := 0; i < parallelDownloadWorkers; i++ {
		start := int64(i) * chunkSize
		if start >= contentLength {
			break
		}
		end := start + chunkSize - 1
		if end >= contentLength {
			end = contentLength - 1
		}

		wg.Add(1)
		go func(start, end int64) {
			defer wg.Done()
			if err := downloadChunk(client, url, start, end, dst, &downloaded, contentLength, sendProgress); err != nil {
				errCh <- err
			}
		}(start, end)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		return err
	}
	return nil
}

// probeRangeSupport issues a HEAD request to check whether the server
// supports HTTP range requests. Returns the content length and true on
// success. Falls back to img.ImageSize if Content-Length is absent.
// Returns 0, false if ranges are unsupported or content length is unknown.
func probeRangeSupport(client *http.Client, img *imageInfo) (contentLength int64, ok bool) {
	resp, err := client.Head(img.DownloadURL)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	// Rejects both absent header and RFC 7233 "Accept-Ranges: none".
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		return 0, false
	}
	cl := resp.ContentLength
	if cl <= 0 && img.ImageSize > 0 {
		cl = img.ImageSize
	}
	if cl <= 0 {
		return 0, false
	}
	return cl, true
}

// downloadImage downloads an OS image to a temp file with a progress bar.
// If the server supports HTTP range requests, it downloads in parallel using
// parallelDownloadWorkers concurrent connections. Falls back to a single
// sequential stream otherwise.
func downloadImage(img *imageInfo) (string, error) {
	client := &http.Client{Timeout: 30 * time.Minute}

	// Write directly into the OS cache directory so we never land in /tmp
	// (which is often a size-limited tmpfs on Linux).
	cacheDir, err := osCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolving cache dir: %w", err)
	}
	tmpFile, err := os.CreateTemp(cacheDir, "wendyos-*.img")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}

	prog := tui.NewProgress(fmt.Sprintf("Downloading %s...", img.Version))
	p := tui.NewProgressProgram(prog)
	sendProgress := throttledProgress(p, 33*time.Millisecond)

	contentLength, supportsRanges := probeRangeSupport(client, img)

	if supportsRanges {
		if err := tmpFile.Truncate(contentLength); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return "", fmt.Errorf("pre-allocating: %w", err)
		}
		go func() {
			p.Send(tui.ProgressDoneMsg{Err: downloadParallel(client, img.DownloadURL, contentLength, tmpFile, sendProgress)})
		}()
	} else {
		go func() {
			resp, err := client.Get(img.DownloadURL)
			if err != nil {
				p.Send(tui.ProgressDoneMsg{Err: fmt.Errorf("downloading: %w", err)})
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				p.Send(tui.ProgressDoneMsg{Err: fmt.Errorf("download returned status %d", resp.StatusCode)})
				return
			}
			total := resp.ContentLength
			if img.ImageSize > 0 {
				total = img.ImageSize
			}
			buf := make([]byte, 1*1024*1024)
			var downloaded int64
			for {
				n, readErr := resp.Body.Read(buf)
				if n > 0 {
					if _, writeErr := tmpFile.Write(buf[:n]); writeErr != nil {
						p.Send(tui.ProgressDoneMsg{Err: writeErr})
						return
					}
					downloaded += int64(n)
					sendProgress(downloaded, total)
				}
				if readErr == io.EOF {
					p.Send(tui.ProgressDoneMsg{})
					return
				}
				if readErr != nil {
					p.Send(tui.ProgressDoneMsg{Err: readErr})
					return
				}
			}
		}()
	}

	finalModel, err := p.Run()
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("progress TUI: %w", err)
	}

	model := finalModel.(tui.ProgressModel)
	if model.Err() != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", model.Err()
	}

	tmpFile.Close()
	return tmpFile.Name(), nil
}

func osCacheDir() (string, error) {
	base, err := config.CacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "os-images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating OS cache directory: %w", err)
	}
	return dir, nil
}

// readFileOrNil reads path, returning nil on error (used for best-effort bmap
// validation where failure just disables the bmap fast path).
func readFileOrNil(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

// osCachedPath builds a cache file path for deviceKey+version, keyed by the
// storage variant ("sd"/"nvme") when one is given so an SD image and an NVMe
// image of the same device+version never share one file. ext includes the dot
// (e.g. ".img", ".zip", ".bmap", ".img.zst"). Inputs are sanitized to prevent
// path traversal from the user-supplied --version flag and the storage key.
func osCachedPath(deviceKey, version, storage, ext string) (string, error) {
	safeDevice := filepath.Base(deviceKey)
	safeVersion := filepath.Base(version)
	if safeDevice != deviceKey || safeVersion != version ||
		strings.Contains(deviceKey, "..") || strings.Contains(version, "..") {
		return "", fmt.Errorf("invalid device key or version: %q / %q", deviceKey, version)
	}
	if storage != "" {
		safeStorage := filepath.Base(storage)
		if safeStorage != storage || strings.Contains(storage, "..") {
			return "", fmt.Errorf("invalid storage key: %q", storage)
		}
	}

	dir, err := osCacheDir()
	if err != nil {
		return "", err
	}
	name := safeVersion
	if storage != "" {
		name = fmt.Sprintf("%s-%s", safeVersion, storage)
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%s%s", safeDevice, name, ext)), nil
}

func osCachedImagePath(deviceKey, version, storage string) (string, error) {
	return osCachedPath(deviceKey, version, storage, ".img")
}

func osCachedZipPath(deviceKey, version, storage string) (string, error) {
	return osCachedPath(deviceKey, version, storage, ".zip")
}

func osCachedBmapPath(deviceKey, version, storage string) (string, error) {
	return osCachedPath(deviceKey, version, storage, ".bmap")
}

// manifestStorage picks the manifest storage key ("sd"/"nvme") for a target
// drive, honoring an explicit override.
//
// A real NVMe controller is unambiguous → nvme. A USB-attached drive is harder:
// an SD card in a USB reader and an NVMe SSD in a USB enclosure both enumerate
// as USB on every platform (the StorageType enum has no SD value), so bus type
// alone can't tell them apart. We disambiguate with mediaFixed — positive
// evidence from the platform lister that the media is fixed, solid-state (an
// SSD), not removable (an SD card / thumb drive). An SSD in a USB enclosure is
// bound for the device's NVMe slot, so when the device also publishes an NVMe
// variant we pick it. Without that evidence we fall back to what the manifest
// publishes via defaultManifestStorage: the SD image when the device ships one
// (Raspberry Pi), else the NVMe image (a Jetson SSD enclosure), else legacy/sd.
// Unknown buses (built-in card readers) → sd. Pass --storage to override.
func manifestStorage(v deviceVersion, st StorageType, mediaFixed bool, override string) string {
	switch override {
	case "nvme", "sd":
		return override
	}
	switch st {
	case StorageNVMe:
		return "nvme"
	case StorageUSB:
		// Fixed, solid-state media behind a USB bridge is an SSD headed for the
		// NVMe slot, not an SD card in a reader. Prefer nvme when the device
		// actually publishes that variant.
		if mediaFixed && v.NVMEPath != "" {
			return "nvme"
		}
		return defaultManifestStorage(v)
	default:
		return "sd"
	}
}

// storageChoiceAmbiguous reports whether the image variant for a USB target
// can't be determined from bus and media signals alone, so the caller should
// ask the user which slot the drive is for. It is only ambiguous when the bus
// is USB (an SD reader and an SSD enclosure look identical), the platform found
// no positive fixed-media evidence, and the device publishes BOTH variants —
// otherwise there is nothing to choose between.
func storageChoiceAmbiguous(v deviceVersion, st StorageType, mediaFixed bool) bool {
	return st == StorageUSB && !mediaFixed && v.SDPath != "" && v.NVMEPath != ""
}

// defaultManifestStorage returns the device's natural removable image variant
// based on which artifacts the manifest version publishes: an SD variant when
// present (the common Raspberry Pi case), otherwise an NVMe variant (a Jetson
// NVMe SSD), otherwise "sd" so resolveTriple falls back to the legacy image.
// Used when there is no target-drive protocol to consult (wendy os download)
// and as the ambiguous-USB tiebreaker for manifestStorage.
func defaultManifestStorage(v deviceVersion) string {
	switch {
	case v.SDPath != "":
		return "sd"
	case v.NVMEPath != "":
		return "nvme"
	default:
		return "sd"
	}
}

func osCachedZstPath(deviceKey, version, storage string) (string, error) {
	return osCachedPath(deviceKey, version, storage, ".img.zst")
}

// resolveSeekableZst downloads (or cache-hits) the seekable .img.zst for
// deviceKey+version from zstURL, returning the cached path. Reuses downloadImage
// (streams to a temp file with progress) then renames into the cache.
func resolveSeekableZst(deviceKey, version, storage, zstURL string) (string, error) {
	cached, err := osCachedZstPath(deviceKey, version, storage)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(cached); statErr == nil && info.Size() > 0 {
		fmt.Printf("Using cached seekable image (%s)\n", cached)
		return cached, nil
	}
	downloadPath, err := downloadImage(&imageInfo{DownloadURL: zstURL, Version: version})
	if err != nil {
		return "", fmt.Errorf("downloading seekable image: %w", err)
	}
	os.Remove(cached) // clear stale/0-byte so Rename succeeds on Windows
	if err := os.Rename(downloadPath, cached); err != nil {
		os.Remove(downloadPath)
		return "", fmt.Errorf("caching seekable image: %w", err)
	}
	return cached, nil
}

type zipReadCloser struct {
	archive *zip.ReadCloser
	entry   io.ReadCloser
}

func (z *zipReadCloser) Read(p []byte) (int, error) { return z.entry.Read(p) }

func (z *zipReadCloser) Close() error {
	err := z.entry.Close()
	if err2 := z.archive.Close(); err == nil {
		err = err2
	}
	return err
}

// streamZipImageEntry opens a zip archive and returns a streaming reader over
// the first .img, .raw, .wic, or .sdimg entry it finds. The zip directory
// stores an exact 64-bit uncompressed size, so the stream carries it for
// byte-accurate progress. The caller must Close the returned reader.
func streamZipImageEntry(zipPath string) (*imageStream, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("opening zip: %w", err)
	}

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name))
		// .sdimg is the Mender A/B disk image RPi targets now produce.
		if ext != ".img" && ext != ".raw" && ext != ".wic" && ext != ".sdimg" {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("opening %s in zip: %w", f.Name, err)
		}

		size := int64(f.UncompressedSize64)
		if size == 0 {
			size = f.FileInfo().Size()
		}
		if size == 0 {
			rc.Close()
			r.Close()
			return nil, fmt.Errorf("zip entry %s has unknown uncompressed size", f.Name)
		}

		return &imageStream{
			ReadCloser:       &zipReadCloser{archive: r, entry: rc},
			uncompressedSize: size,
		}, nil
	}

	r.Close()
	return nil, fmt.Errorf("no .img, .raw, .wic, or .sdimg file found in zip archive")
}

func resolveOSImage(deviceKey string, img *imageInfo) (string, error) {
	isZip := strings.HasSuffix(strings.ToLower(img.DownloadURL), ".zip")

	// Legacy .img cache hit (backward compat with pre-streaming caches).
	imgCached, err := osCachedImagePath(deviceKey, img.Version, img.Storage)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(imgCached); statErr == nil && info.Size() > 0 {
		fmt.Printf("Using cached image (%s)\n", imgCached)
		return imgCached, nil
	}

	if isZip {
		// Zip cache hit.
		zipCached, zipErr := osCachedZipPath(deviceKey, img.Version, img.Storage)
		if zipErr != nil {
			return "", zipErr
		}
		if info, statErr := os.Stat(zipCached); statErr == nil && info.Size() > 0 {
			fmt.Printf("Using cached image (%s)\n", zipCached)
			return zipCached, nil
		}
		// Cache miss: download zip, rename to zip cache path.
		downloadPath, dlErr := downloadImage(img)
		if dlErr != nil {
			return "", fmt.Errorf("downloading image: %w", dlErr)
		}
		os.Remove(zipCached) // remove stale/0-byte file if present so Rename succeeds on Windows
		if renameErr := os.Rename(downloadPath, zipCached); renameErr != nil {
			os.Remove(downloadPath)
			return "", fmt.Errorf("caching image: %w", renameErr)
		}
		return zipCached, nil
	}

	// Non-zip URL: download img directly and cache as .img.
	downloadPath, err := downloadImage(img)
	if err != nil {
		return "", fmt.Errorf("downloading image: %w", err)
	}
	os.Remove(imgCached) // remove stale/0-byte file if present so Rename succeeds on Windows
	if err := os.Rename(downloadPath, imgCached); err != nil {
		os.Remove(downloadPath)
		return "", fmt.Errorf("caching image: %w", err)
	}
	return imgCached, nil
}

// imageStream couples a streaming image reader with the size information
// available for driving a progress bar. Exactly one of two progress modes
// applies: when uncompressedSize is non-zero it is exact and the bar tracks
// decompressed bytes written; otherwise compressedRead/compressedSize track
// how much of the compressed source has been consumed.
type imageStream struct {
	io.ReadCloser
	// uncompressedSize is the exact decompressed byte count, or 0 when the
	// source cannot report one reliably.
	uncompressedSize int64
	// compressedRead reports compressed source bytes consumed so far.
	// nil when uncompressedSize is known.
	compressedRead func() int64
	// compressedSize is the total compressed source size in bytes.
	compressedSize int64
	// sourcePath is the compressed file on disk; set when uncompressedSize
	// can be determined by measureUncompressedSize.
	sourcePath string
}

// measureUncompressedSize determines the exact decompressed size by
// decompressing the source once and counting the bytes, then records it in a
// sidecar file so later flashes of the same image skip the pass. No-op when
// the size is already known or the source is not measurable. progress
// receives (compressedBytesRead, compressedTotal).
func (s *imageStream) measureUncompressedSize(progress func(read, total int64)) error {
	if s.uncompressedSize > 0 || s.sourcePath == "" {
		return nil
	}
	size, err := measureGzipImage(s.sourcePath, progress)
	if err != nil {
		return err
	}
	s.uncompressedSize = size
	writeImageSizeSidecar(s.sourcePath, s.compressedSize, size)
	return nil
}

// measureGzipImage decompresses the gzip file at path, discarding the bytes,
// and returns the exact decompressed size. This is the only reliable way to
// size a gzip stream: the ISIZE trailer is truncated mod 2^32, and the
// compressed-consumption fraction is wildly nonlinear for zero-heavy disk
// images. The pass is CPU-bound and costs a few tens of seconds for a
// multi-GiB image, against minutes of disk writing.
func measureGzipImage(path string, progress func(read, total int64)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("opening gzip image: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat gzip image: %w", err)
	}

	cr := &countingReader{r: f}
	gr, err := gzip.NewReader(cr)
	if err != nil {
		return 0, fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gr.Close()

	var size int64
	buf := make([]byte, 1<<20)
	for {
		n, readErr := gr.Read(buf)
		size += int64(n)
		if progress != nil && n > 0 {
			progress(cr.n.Load(), info.Size())
		}
		if readErr == io.EOF {
			return size, nil
		}
		if readErr != nil {
			return 0, fmt.Errorf("decompressing image: %w", readErr)
		}
	}
}

// measureImageWithProgress runs stream.measureUncompressedSize behind a
// progress bar tracking compressed bytes read. Returns context.Canceled when
// the user quits the bar.
func measureImageWithProgress(stream *imageStream) error {
	prog := tui.NewProgress("Determining image size (one-time per image)...")
	p := tui.NewProgressProgram(prog)
	sendProgress := throttledProgress(p, 33*time.Millisecond)

	go func() {
		p.Send(tui.ProgressDoneMsg{Err: stream.measureUncompressedSize(sendProgress)})
	}()

	final, err := p.Run()
	if err != nil {
		return fmt.Errorf("progress TUI: %w", err)
	}
	return final.(tui.ProgressModel).Err()
}

// imageSizeSidecar caches a measured decompressed size next to the compressed
// image, keyed to the compressed size so a replaced file invalidates it.
type imageSizeSidecar struct {
	CompressedSize   int64 `json:"compressed_size"`
	UncompressedSize int64 `json:"uncompressed_size"`
}

func imageSizeSidecarPath(imagePath string) string { return imagePath + ".size" }

// readImageSizeSidecar returns the cached decompressed size for imagePath,
// or 0 when no valid sidecar exists for the given compressed size.
func readImageSizeSidecar(imagePath string, compressedSize int64) int64 {
	data, err := os.ReadFile(imageSizeSidecarPath(imagePath))
	if err != nil {
		return 0
	}
	var sc imageSizeSidecar
	if json.Unmarshal(data, &sc) != nil || sc.CompressedSize != compressedSize || sc.UncompressedSize <= 0 {
		return 0
	}
	return sc.UncompressedSize
}

// writeImageSizeSidecar persists a measured size. Best-effort: a failure just
// means the next flash measures again.
func writeImageSizeSidecar(imagePath string, compressedSize, uncompressedSize int64) {
	data, err := json.Marshal(imageSizeSidecar{
		CompressedSize:   compressedSize,
		UncompressedSize: uncompressedSize,
	})
	if err != nil {
		return
	}
	_ = os.WriteFile(imageSizeSidecarPath(imagePath), data, 0o644)
}

// writeProgressMsg converts dd's running byte count into a progress update.
// Returns false when the stream carries no usable size information.
func (s *imageStream) writeProgressMsg(written int64) (tui.ProgressUpdateMsg, bool) {
	switch {
	case s.uncompressedSize > 0:
		return tui.ProgressUpdateMsg{
			Percent: float64(written) / float64(s.uncompressedSize),
			Written: written,
			Total:   s.uncompressedSize,
		}, true
	case s.compressedRead != nil && s.compressedSize > 0:
		// Decompression is demand-driven by dd's reads, so the fraction of
		// compressed input consumed tracks write completion. Total stays 0:
		// the decompressed size is unknown and must not be guessed at.
		return tui.ProgressUpdateMsg{
			Percent: float64(s.compressedRead()) / float64(s.compressedSize),
			Written: written,
		}, true
	default:
		return tui.ProgressUpdateMsg{}, false
	}
}

// countingReader counts bytes read through it. The count is read from a
// progress goroutine while Read is called from the dd-feeding goroutine,
// hence the atomic.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// gzipReadCloser wraps a gzip.Reader so that closing it also closes the
// underlying file, matching the io.ReadCloser contract.
type gzipReadCloser struct {
	gz *gzip.Reader
	f  *os.File
}

func (g *gzipReadCloser) Read(p []byte) (int, error) { return g.gz.Read(p) }
func (g *gzipReadCloser) Close() error {
	err := g.gz.Close()
	if err2 := g.f.Close(); err == nil {
		err = err2
	}
	return err
}

// streamGzipImage opens a gzip-compressed image file and returns a streaming
// reader over the decompressed bytes. The gzip format cannot report the
// uncompressed size of large images — its ISIZE trailer is stored mod 2^32,
// so a 19.2 GiB image reports 3.2 GiB and the progress bar overshoots its
// total. The exact size comes from a sidecar written by a previous
// measureUncompressedSize pass when available; otherwise it is 0 and the
// caller should measure (or fall back to compressed-consumption progress).
func streamGzipImage(path string) (*imageStream, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening gzip image: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat gzip image: %w", err)
	}

	cr := &countingReader{r: f}
	gr, err := gzip.NewReader(cr)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("creating gzip reader: %w", err)
	}
	return &imageStream{
		ReadCloser:       &gzipReadCloser{gz: gr, f: f},
		uncompressedSize: readImageSizeSidecar(path, info.Size()),
		compressedRead:   cr.n.Load,
		compressedSize:   info.Size(),
		sourcePath:       path,
	}, nil
}

// isGzipFile returns true when path begins with the gzip magic bytes (0x1f 0x8b).
// Used to detect compressed images regardless of file extension — GCS images
// are sometimes served as .img.gz but cached under the .img extension.
func isGzipFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [2]byte
	_, err = io.ReadFull(f, magic[:])
	return err == nil && magic[0] == 0x1f && magic[1] == 0x8b
}

// openOSImageStream resolves the cached file for deviceKey+img, then returns
// a streaming reader over the image bytes. The caller must Close it.
func openOSImageStream(deviceKey string, img *imageInfo) (*imageStream, error) {
	cachePath, err := resolveOSImage(deviceKey, img)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(strings.ToLower(cachePath), ".zip") {
		return streamZipImageEntry(cachePath)
	}
	if isGzipFile(cachePath) {
		return streamGzipImage(cachePath)
	}
	return openRawImageStream(cachePath)
}

// openLocalImageStream opens an arbitrary local file for streaming.
// If the path ends in .zip it finds the first image entry inside it.
// Otherwise it opens the file directly as a reader.
func openLocalImageStream(imagePath string) (*imageStream, error) {
	if strings.HasSuffix(strings.ToLower(imagePath), ".zip") {
		return streamZipImageEntry(imagePath)
	}
	if isGzipFile(imagePath) {
		return streamGzipImage(imagePath)
	}
	return openRawImageStream(imagePath)
}

// openRawImageStream opens an uncompressed image file; its size on disk is
// the exact byte count that will be written.
func openRawImageStream(path string) (*imageStream, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening image: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat image: %w", err)
	}
	return &imageStream{ReadCloser: f, uncompressedSize: info.Size()}, nil
}

// wifiCLIOptions captures the WiFi-related flags coming from cobra so they
// can be threaded through as a single value.
type wifiCLIOptions struct {
	SSID     string   // --wifi-ssid shortcut
	Password string   // --wifi-password (only valid with --wifi-ssid)
	Entries  []string // --wifi, repeatable
	NoWifi   bool     // --no-wifi
}

// resolveWiFiCredentialsList builds the ordered list of WiFi credentials to
// write to the config partition. It consults flags first (non-interactive),
// and only falls back to the Bubble Tea prompts when no flag was set and
// stdin is a TTY.
func resolveWiFiCredentialsList(opts wifiCLIOptions) ([]wendyconf.WifiCredential, error) {
	if opts.NoWifi {
		if opts.SSID != "" || len(opts.Entries) > 0 {
			return nil, fmt.Errorf("--no-wifi is incompatible with --wifi / --wifi-ssid")
		}
		return nil, nil
	}
	if opts.Password != "" && opts.SSID == "" {
		return nil, fmt.Errorf("--wifi-password requires --wifi-ssid")
	}

	var creds []wendyconf.WifiCredential

	// --wifi (repeatable) first so priorities stay in the order the user typed.
	for _, raw := range opts.Entries {
		c, err := parseWiFiEntry(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid --wifi %q: %w", raw, err)
		}
		creds = append(creds, c)
	}

	// --wifi-ssid shortcut folds into a single trailing entry.
	if opts.SSID != "" {
		c := wendyconf.WifiCredential{SSID: opts.SSID, Password: opts.Password}
		if c.Password == "" {
			if pw, kerr := lookupKeychainPassword(c.SSID); kerr == nil && pw != "" {
				c.Password = pw
			} else if isInteractiveTerminal() {
				pw, perr := tui.PromptPassword(fmt.Sprintf("WiFi password for %s", c.SSID), "(leave empty for open network)", nil)
				if perr != nil {
					return nil, fmt.Errorf("reading WiFi password: %w", perr)
				}
				c.Password = pw
			}
		}
		creds = append(creds, c)
	}

	// If any flag supplied creds OR stdin is not a TTY, we're done.
	if len(creds) > 0 || !isInteractiveTerminal() {
		return creds, nil
	}

	// Interactive path: Y/N → loop until the user declines another network.
	enable, err := tui.ConfirmDefaultYes("Set up WiFi on first boot?")
	if err != nil {
		return nil, err
	}
	if !enable {
		return nil, nil
	}

	for {
		c, added, err := promptAddOneCredential(len(creds))
		if err != nil {
			return nil, err
		}
		if !added {
			break
		}
		creds = append(creds, c)

		more, err := tui.Confirm("Add another WiFi network?")
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
	}

	return creds, nil
}

// parseWiFiEntry parses `ssid=X,password=Y,priority=N,hidden=true,security=wpa2`.
// Only `ssid=` is required; commas inside values can be escaped with `\,`.
func parseWiFiEntry(raw string) (wendyconf.WifiCredential, error) {
	var c wendyconf.WifiCredential
	for _, kv := range splitEscaped(raw, ',') {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			return c, fmt.Errorf("expected key=value, got %q", kv)
		}
		k := strings.ToLower(strings.TrimSpace(kv[:eq]))
		v := strings.TrimSpace(kv[eq+1:])
		switch k {
		case "ssid":
			c.SSID = v
		case "password", "pass", "psk":
			c.Password = v
		case "priority":
			n, err := strconv.Atoi(v)
			if err != nil {
				return c, fmt.Errorf("priority must be an integer: %w", err)
			}
			c.Priority = int32(n)
		case "hidden":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return c, fmt.Errorf("hidden must be true/false: %w", err)
			}
			c.Hidden = b
		case "security":
			c.Security = strings.ToLower(v)
		default:
			return c, fmt.Errorf("unknown key %q", k)
		}
	}
	if c.SSID == "" {
		return c, fmt.Errorf("ssid is required")
	}
	return c, nil
}

// splitEscaped splits s on sep, honouring `\sep` as a literal separator char.
func splitEscaped(s string, sep byte) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == sep {
			cur.WriteByte(sep)
			i++
			continue
		}
		if s[i] == sep {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	out = append(out, cur.String())
	return out
}

// Interactive hooks used by promptAddOneCredential, declared as package vars
// so unit tests can stub them (same pattern as promptDeviceName below).
var (
	selectWifiNetworkFromScan = selectWifiNetworkStreaming
	confirmManualWifiEntry    = func() (bool, error) {
		return tui.ConfirmDefaultYes("Enter WiFi network details manually?")
	}
	confirmKeychainLookup = func(ssid string) (bool, error) {
		return tui.ConfirmDefaultYes(fmt.Sprintf("Look up password for '%s' from keychain? (macOS will ask for permission)", ssid))
	}
	promptWifiSSID = func() (string, error) {
		return tui.PromptText("WiFi SSID", "", nonEmptyValidator)
	}
	promptWifiPassword = func(ssid string) (string, error) {
		return tui.PromptPassword(fmt.Sprintf("Password for %s", ssid), "(leave empty for open network)", nil)
	}
)

// wifiScanSelection is the outcome of the streaming scan-and-pick step.
type wifiScanSelection struct {
	SSID        string // empty when the user made no selection
	HadNetworks bool   // the scan returned at least one network
	ScanErr     error  // scan failure recorded while the picker was open
}

// selectWifiNetworkStreaming shows the WiFi picker immediately and streams
// the scan results in: the CoreWLAN/nmcli/netsh scan can take several
// seconds, and a visible "Scanning..." list reads better than blocking
// before any UI appears.
func selectWifiNetworkStreaming() (wifiScanSelection, error) {
	var sel wifiScanSelection

	picker := tui.NewPickerWithTitleAndColumns("Select WiFi network (or esc to type manually)", wifiPickerColumns())
	picker.Filterable = true
	p := tea.NewProgram(picker)

	// The user can quit the picker before the scan goroutine finishes, so
	// every access to sel is mutex-guarded.
	var mu sync.Mutex
	go func() {
		defer p.Send(tui.PickerDoneMsg{})
		// Stream the host scan: cached results paint the picker instantly, then
		// the fresh rescan fills it in — so SSIDs trickle in rather than the
		// picker sitting on "Scanning..." until the whole scan completes
		// (matching the device-side picker in pickWifiNetwork).
		hadNetworks := false
		err := streamLocalWifiScan(func(batch []localWifiNetwork) {
			if len(batch) > 0 {
				hadNetworks = true
			}
			p.Send(tui.PickerAddMsg{Items: localWifiPickerItems(batch)})
		})
		mu.Lock()
		sel.ScanErr = err
		sel.HadNetworks = hadNetworks
		mu.Unlock()
	}()
	fmt.Println()
	finalModel, runErr := p.Run()
	if runErr != nil {
		return wifiScanSelection{}, fmt.Errorf("scanning WiFi networks: %w", runErr)
	}
	pm, ok := finalModel.(tui.PickerModel)
	if !ok {
		return wifiScanSelection{}, fmt.Errorf("scanning WiFi networks: unexpected picker model %T", finalModel)
	}
	mu.Lock()
	defer mu.Unlock()
	if picked := pm.Selected(); picked != nil {
		sel.SSID, _ = picked.Value.(string)
	}
	return sel, nil
}

// promptAddOneCredential runs the local scan + picker + password prompt to
// collect a single WiFi credential. index is the zero-based count of entries
// already collected (used to suggest a descending priority). Returns
// added=false (with nil error) when the user chooses to skip WiFi setup
// after a failed or empty scan (WDY-1474).
func promptAddOneCredential(index int) (wendyconf.WifiCredential, bool, error) {
	var c wendyconf.WifiCredential

	sel, err := selectWifiNetworkFromScan()
	if err != nil {
		return c, false, err
	}
	c.SSID = sel.SSID

	// No selection and nothing usable was scanned: say why, then let the
	// user choose between manual entry and skipping WiFi setup entirely
	// instead of trapping them in a mandatory SSID prompt (WDY-1474). A
	// deliberate esc while networks are listed falls straight through to
	// the manual prompt, as the picker title advertises.
	if c.SSID == "" && (sel.ScanErr != nil || !sel.HadNetworks) {
		fmt.Println(wifiScanFailureNotice(sel.ScanErr))
		manual, err := confirmManualWifiEntry()
		if err != nil {
			return c, false, err
		}
		if !manual {
			fmt.Println("Skipping WiFi setup. You can configure WiFi later with 'wendy wifi connect' once the device is reachable (e.g. over ethernet or USB).")
			return c, false, nil
		}
	}

	if c.SSID == "" {
		ssid, err := promptWifiSSID()
		if err != nil {
			return c, false, err
		}
		c.SSID = ssid
	}

	if supportsKeychainLookup {
		useKeychain, err := confirmKeychainLookup(c.SSID)
		if err != nil {
			return c, false, err
		}
		if useKeychain {
			if pw, kerr := lookupKeychainPassword(c.SSID); kerr == nil && pw != "" {
				fmt.Println("Using saved password from keychain.")
				c.Password = pw
			} else {
				fmt.Println("Password not available from keychain.")
			}
		}
	}

	if c.Password == "" {
		pw, err := promptWifiPassword(c.SSID)
		if err != nil {
			return c, false, err
		}
		c.Password = pw
	}

	// First network gets the highest implicit priority; each subsequent one
	// steps down. Users can still override via the non-interactive flags.
	c.Priority = int32(100 - index)
	return c, true, nil
}

func nonEmptyValidator(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("required")
	}
	return nil
}

// maxDeviceNameLen caps the device name so the derived hostname stays a valid
// DNS label. The agent builds the hostname as "wendyos-<name>" (see
// generate-hostname.sh / configpartition.applyDeviceName); with the 8-character
// "wendyos-" prefix, a 55-character name yields a 63-octet label — the RFC 1035
// maximum. Longer names produce an invalid hostname label on the device.
const maxDeviceNameLen = 55

func validateDeviceName(name string) error {
	if len(name) < 3 || len(name) > maxDeviceNameLen {
		return fmt.Errorf("device name must be 3–%d characters", maxDeviceNameLen)
	}
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z':
		case (c >= '0' && c <= '9') || c == '-':
			if i == 0 {
				return fmt.Errorf("device name must start with a lowercase letter")
			}
		default:
			return fmt.Errorf("device name may only contain lowercase letters, digits, and hyphens")
		}
	}
	return nil
}

func optionalDeviceNameValidator(name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	return validateDeviceName(name)
}

var promptDeviceName = func(prompt, hint string, validate tui.ValidateFunc) (string, error) {
	return tui.PromptText(prompt, hint, validate)
}

func resolveDeviceName(flagName string) (string, error) {
	if flagName != "" {
		if err := validateDeviceName(flagName); err != nil {
			return "", fmt.Errorf("--device-name: %w", err)
		}
		return flagName, nil
	}

	if !isInteractiveTerminal() {
		return "", nil
	}

	fmt.Println()
	name, err := promptDeviceName(
		"Device name",
		fmt.Sprintf("(a-z, 0-9 and hyphens, starts with a letter, 3–%d chars; empty = auto-generate)", maxDeviceNameLen),
		optionalDeviceNameValidator,
	)
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			return "", ErrUserCancelled
		}
		return "", fmt.Errorf("device-name prompt: %w", err)
	}
	return strings.TrimSpace(name), nil
}

// confirmOverwriteInternalDrive guards against accidentally wiping internal
// (non-removable) drives. The OS-level system disk is already filtered out by
// listAllDrives, but a non-system internal drive (e.g. a secondary SATA SSD
// on Windows) is still selectable via --drive — the existing y/n confirm is
// too easy to autopilot through. For those drives we either require an
// explicit --yes-overwrite-internal flag (non-interactive) or a typed device
// path (interactive).
func confirmOverwriteInternalDrive(d drive, force bool, yesOverwriteInternal bool) error {
	if d.IsRemovable {
		return nil
	}
	if yesOverwriteInternal {
		return nil
	}
	if force {
		return fmt.Errorf("refusing to wipe non-removable drive %s (%s) with --force; pass --yes-overwrite-internal to confirm you really want to overwrite an internal drive", d.Name, d.DevicePath)
	}
	fmt.Printf("\n%s (%s) is an internal (non-removable) drive.\n", d.Name, d.DevicePath)
	fmt.Println("Typically WendyOS is installed to an SD card or USB drive — overwriting an internal drive will destroy whatever filesystem currently lives on it.")
	fmt.Printf("To proceed, type the device path exactly:\n  %s\n> ", d.DevicePath)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	if strings.TrimSpace(line) != d.DevicePath {
		return fmt.Errorf("internal-drive overwrite cancelled (typed value did not match %s)", d.DevicePath)
	}
	return nil
}

// provisionConfigPartitionFn is the provisioning entry point used by
// provisionConfigWithRetry. It is a package var so tests can stub the real
// network + disk work it performs.
var provisionConfigPartitionFn = provisionConfigPartition

// confirmProvisioningRetry asks whether to re-attempt config-partition
// provisioning after a failure. Declared as a var so tests can drive the loop.
var confirmProvisioningRetry = func() (bool, error) {
	return tui.Confirm("Retry writing provisioning data to the config partition?")
}

// provisionConfigWithRetry writes provisioning data — the agent binary, WiFi
// credentials, device name, and pre-enrollment material — to the config
// partition of a freshly imaged drive.
//
// A provisioning failure is never fatal. By the time we get here the OS image
// is already written to the drive, so the device boots regardless: it runs the
// agent baked into the image and fetches updates and configuration after first
// boot. Treating "couldn't download the agent update" or "couldn't locate the
// config partition" as a failure to flash the OS is misleading — so we surface
// it as a warning instead, loudly when the user explicitly asked for --wifi /
// --device-name / --pre-enroll (that input did not reach the device), and on an
// interactive terminal we offer to retry — e.g. after re-seating an SD card
// whose config partition could not be located.
func provisionConfigWithRetry(d drive, creds []wendyconf.WifiCredential, deviceName string, provisioningJSON []byte, requested bool) {
	for {
		err := provisionConfigPartitionFn(d, creds, deviceName, provisioningJSON)
		if err == nil {
			return
		}

		if requested {
			fmt.Printf("\nWarning: could not apply provisioning data — --wifi / --device-name / --pre-enroll were requested but not written: %v\n", err)
		} else {
			fmt.Printf("\nWarning: could not pre-configure the agent on the config partition: %v\n", err)
		}
		fmt.Println("The OS image itself was written successfully — the device will still boot, run the agent baked into the image, and fetch updates after first boot.")

		if !isInteractiveTerminal() {
			if requested {
				fmt.Println("Re-run 'wendy os install' to apply WiFi / device-name / pre-enrollment, or configure the device after it boots.")
			}
			return
		}

		retry, err := confirmProvisioningRetry()
		if err != nil || !retry {
			if requested {
				fmt.Println("Skipping provisioning. Re-run 'wendy os install' to apply it, or configure the device after it boots.")
			}
			return
		}
	}
}

// provisionConfigPartition downloads the latest stable arm64 wendy-agent binary
func provisionConfigPartition(d drive, creds []wendyconf.WifiCredential, deviceName string, provisioningJSON []byte) error {
	release, err := fetchAgentRelease(false)
	if err != nil {
		return fmt.Errorf("fetching latest agent release: %w", err)
	}

	const assetPrefix = "wendy-agent-linux-arm64-"
	var matched *githubReleaseAsset
	for i := range release.Assets {
		a := &release.Assets[i]
		if strings.HasPrefix(a.Name, assetPrefix) && strings.HasSuffix(a.Name, ".tar.gz") {
			matched = a
			break
		}
	}
	if matched == nil {
		return fmt.Errorf("no arm64 asset found in release %s", release.TagName)
	}

	fmt.Printf("Downloading wendy-agent %s for device...\n", release.TagName)
	agentBinary, err := downloadAgentBinary(*matched)
	if err != nil {
		return fmt.Errorf("downloading agent binary: %w", err)
	}

	return writeConfigPartition(d, agentBinary, creds, deviceName, provisioningJSON)
}

// installESP32Firmware handles the ESP32 path: detect device → download → flash.
// chip is e.g. "esp32c6" or "esp32c5".
func installESP32Firmware(ctx context.Context, nightly bool, chip string, wifi wifiCLIOptions, deviceName string, preOpts preEnrollOptions) error {
	provCreds, err := resolveWiFiCredentialsList(wifi)
	if err != nil {
		return err
	}

	provDeviceName, err := resolveDeviceName(deviceName)
	if err != nil {
		return err
	}

	var enrolledState *PreProvisionedState
	if preOpts.mode != preEnrollSkip {
		cfg, cfgErr := config.Load()
		if cfgErr != nil {
			if preOpts.mode == preEnrollForced {
				return fmt.Errorf("--pre-enroll: loading config: %w", cfgErr)
			}
			cfg = &config.Config{} // auto mode: treat an unreadable config as not logged in
		}
		var resolveErr error
		enrolledState, resolveErr = resolvePreEnrollment(ctx, cfg, preOpts, isInteractiveTerminal(), provDeviceName)
		if resolveErr != nil {
			return resolveErr
		}
	}

	wendyConf, err := buildWendyConf(provCreds, provDeviceName, enrolledState)
	if err != nil {
		return fmt.Errorf("building Wendy config: %w", err)
	}

	if wendyConf.Wifi != nil && len(wendyConf.Wifi.Networks) > 1 {
		return fmt.Errorf("this device only supports one Wi-Fi network")
	}

	fmt.Println("\nScanning for ESP32 devices...")

	serialPort, err := discovery.ResolveESP32SerialPort()
	if err != nil {
		fmt.Println("\nNo ESP32 device detected.")
		fmt.Println("Make sure your ESP32 is connected via USB and in bootloader mode.")
		fmt.Println("To enter bootloader mode: hold the BOOT button, press RESET, then release BOOT.")
		return fmt.Errorf("ESP32 not found: %w", err)
	}

	fmt.Printf("Found ESP32 at %s\n", serialPort)

	fmt.Println("Fetching latest Wendy Lite firmware...")
	asset, err := fetchFirmwareFromManifest(chip, nightly)
	if err != nil {
		return fmt.Errorf("fetching firmware: %w", err)
	}
	fmt.Printf("Found firmware: %s v%s\n", asset.Name, asset.Version)

	// Download with progress bar.

	prog := tui.NewProgress(fmt.Sprintf("Downloading %s %s...", asset.Name, asset.Version))
	p := tui.NewProgressProgram(prog)

	var fwPath string
	var dlErr error

	go func() {
		fwPath, dlErr = downloadFirmware(asset, func(downloaded, total int64) {
			if total > 0 {
				p.Send(tui.ProgressUpdateMsg{Percent: float64(downloaded) / float64(total)})
			}
		})
		if dlErr != nil {
			p.Send(tui.ProgressDoneMsg{Err: dlErr})
		} else {
			p.Send(tui.ProgressDoneMsg{})
		}
	}()

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("progress TUI: %w", err)
	}

	model := finalModel.(tui.ProgressModel)
	if model.Err() != nil {
		return model.Err()
	}
	defer os.Remove(fwPath)

	// Include configuration into the flash image.

	img, err := LoadEspFlashImage(fwPath)
	if err != nil {
		return fmt.Errorf("loading firmware image: %w", err)
	}

	confBytes, err := proto.Marshal(wendyConf)
	if err != nil {
		return fmt.Errorf("serializing device config: %w", err)
	}
	payload := make([]byte, 8+len(confBytes))
	copy(payload[0:4], "WYC0")
	binary.LittleEndian.PutUint32(payload[4:8], uint32(len(confBytes)))
	copy(payload[8:], confBytes)
	if err := img.SetPartition("wendy_conf", payload); err != nil {
		return fmt.Errorf("writing config to firmware image: %w", err)
	}

	// Flash with progress bar.

	fmt.Println()
	flashProg := tui.NewProgress(fmt.Sprintf("Flashing to %s...", serialPort))
	fp := tui.NewProgressProgram(flashProg)

	go func() {
		flashErr := flashFirmwareImage(serialPort, img, func(pct float64) {
			fp.Send(tui.ProgressUpdateMsg{Percent: pct})
		})
		fp.Send(tui.ProgressDoneMsg{Err: flashErr})
	}()

	flashFinal, err := fp.Run()
	if err != nil {
		return fmt.Errorf("flash TUI: %w", err)
	}

	flashModel := flashFinal.(tui.ProgressModel)
	if flashModel.Err() != nil {
		return fmt.Errorf("flashing failed: %w", flashModel.Err())
	}

	fmt.Printf("\nSuccessfully flashed Wendy Lite %s!\n", asset.Version)
	fmt.Println("The device will reboot automatically.")
	return nil
}
