//go:build linux

package commands

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/rcm"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

const (
	unitreeG1Version          = "6.2"
	unitreeG1ImageName        = "g1-nx-j6.2.img.bz2"
	unitreeG1FirmwareName     = "Jetpack_6.2_nx.tar.bz2"
	unitreeG1MinDriveBytes    = int64(900_000_000_000)
	unitreeG1HistoricalSource = "https://drive.google.com/drive/folders/1ho17ectOxi7FbaRFdpAbP4tet8BJWjbm"
)

type unitreeG1Packages struct {
	Directory string
	Image     string
	Firmware  string
}

func installUnitreeG1(ctx context.Context, opts unitreeG1InstallOptions) error {
	if runtime.GOARCH != "amd64" {
		return fmt.Errorf("Unitree G1 flashing currently requires an Ubuntu x86-64 host; this host is linux/%s", runtime.GOARCH)
	}
	if !isInteractiveTerminal() {
		return fmt.Errorf("the experimental Unitree G1 installer currently requires an interactive terminal")
	}

	version, err := selectUnitreeG1Version(opts.Version)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(tui.Header("Install Unitree G1 PC2 · JetPack " + version))
	fmt.Println(tui.WarningMessage("Experimental hardware flow: this erases a replacement NVMe and updates PC2 module firmware. It never flashes the G1 motion-control computer."))
	fmt.Println("The original G1 NVMe must be removed and preserved before continuing.")
	fmt.Println("Historical lab package source: " + unitreeG1HistoricalSource)
	fmt.Println()

	packageDir, err := tui.PromptTextWithDefault(
		"Unitree package folder",
		fmt.Sprintf("must contain %s and %s", unitreeG1ImageName, unitreeG1FirmwareName),
		".",
		func(value string) error {
			_, err := resolveUnitreeG1Packages(value)
			return err
		},
	)
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			return ErrUserCancelled
		}
		return err
	}
	packages, err := resolveUnitreeG1Packages(packageDir)
	if err != nil {
		return err
	}

	if err := preAuthElevation(); err != nil {
		return err
	}
	target, err := selectUnitreeG1Drive(ctx, opts.Drive)
	if err != nil {
		return err
	}
	if err := validateUnitreeG1Drive(target); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(tui.Header("Review destructive write"))
	fmt.Printf("  Model:  %s\n", target.Name)
	if target.Serial != "" {
		fmt.Printf("  Serial: %s\n", target.Serial)
	}
	fmt.Printf("  Device: %s\n", target.DevicePath)
	fmt.Printf("  Size:   %s\n", target.Size)
	fmt.Printf("  Image:  %s\n", filepath.Base(packages.Image))
	fmt.Printf("  PC2:    %s\n", filepath.Base(packages.Firmware))

	if !opts.Force {
		fmt.Println()
		confirmed, err := tui.Confirm("Erase this replacement NVMe and write the Unitree G1 image?")
		if err != nil {
			return err
		}
		if !confirmed {
			return ErrUserCancelled
		}
	}

	stream, err := streamBzip2Image(packages.Image)
	if err != nil {
		return err
	}
	defer stream.Close()
	if err := writeUnitreeG1Image(stream, target); err != nil {
		return err
	}
	ejectDisk(target)

	fmt.Println()
	fmt.Println(tui.SuccessMessage("The Unitree G1 image was written to the replacement NVMe."))
	fmt.Println("  1. Power off the G1.")
	fmt.Println("  2. Remove the replacement NVMe from its USB enclosure.")
	fmt.Println("  3. Install it securely in PC2; keep the original NVMe unchanged.")
	fmt.Println("  4. Power on the G1 and wait for all three power indicators to stay steady.")
	fmt.Println()
	ready, err := tui.Confirm("Is the replacement NVMe installed and the G1 ready for PC2 recovery mode?")
	if err != nil {
		return err
	}
	if !ready {
		return ErrUserCancelled
	}

	fmt.Println()
	fmt.Println(tui.Header("Put G1 PC2 into recovery mode"))
	fmt.Println("  1. Hold PC2 PWR and REC together for about 2 seconds.")
	fmt.Println("  2. Release PWR while continuing to hold REC for another 2 seconds.")
	fmt.Println("  3. Release REC.")
	fmt.Println("  4. Connect this host to PC2's dedicated USB-C flashing port with a data cable.")
	fmt.Println("  5. Keep the G1 powered and USB connected until the firmware script reports success.")
	fmt.Println()
	ready, err = tui.Confirm("Start scanning for the G1's Orin NX in APX recovery mode?")
	if err != nil {
		return err
	}
	if !ready {
		return ErrUserCancelled
	}

	dev, err := pickUnitreeG1RecoveryDevice()
	if err != nil {
		return err
	}
	fmt.Println(tui.SuccessMessage("Detected " + dev.Describe() + " in APX recovery mode."))

	workspace, script, err := extractUnitreeG1Firmware(ctx, packages.Firmware)
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)

	fmt.Println()
	fmt.Println(tui.Header("Flashing matching Unitree PC2 module firmware"))
	fmt.Println(tui.WarningMessage("Do not power off the G1 or disconnect USB until flash_nx_module.sh reports success."))
	if err := runUnitreeG1FirmwareScript(ctx, script); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(tui.SuccessMessage("Unitree G1 PC2 image and module firmware completed."))
	fmt.Println("Power off the G1, disconnect recovery USB, then boot normally and allow several minutes for first boot.")
	fmt.Println("Verify with: ping 192.168.123.164 && ssh unitree@192.168.123.164")
	return nil
}

func selectUnitreeG1Version(requested string) (string, error) {
	if requested != "" && requested != unitreeG1Version {
		return "", fmt.Errorf("Unitree G1 version %q is unavailable; this experimental installer supports %s", requested, unitreeG1Version)
	}
	if requested != "" || !isInteractiveTerminal() {
		return unitreeG1Version, nil
	}
	return pickFromItems("Select an installation", []tui.PickerItem{
		{
			Name:        "Unitree G1 JetPack 6.2",
			Description: "Ubuntu 22.04 · Jetson Linux R36.4.3 · PC2 Orin NX",
			Section:     "Unitree G1",
			Value:       unitreeG1Version,
		},
	})
}

func resolveUnitreeG1Packages(dir string) (unitreeG1Packages, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return unitreeG1Packages{}, fmt.Errorf("package folder is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return unitreeG1Packages{}, fmt.Errorf("resolving package folder: %w", err)
	}
	result := unitreeG1Packages{
		Directory: abs,
		Image:     filepath.Join(abs, unitreeG1ImageName),
		Firmware:  filepath.Join(abs, unitreeG1FirmwareName),
	}
	for _, path := range []string{result.Image, result.Firmware} {
		info, err := os.Lstat(path)
		if err != nil {
			return unitreeG1Packages{}, fmt.Errorf("required package %s: %w", filepath.Base(path), err)
		}
		if !info.Mode().IsRegular() {
			return unitreeG1Packages{}, fmt.Errorf("required package %s must be a regular file, not a directory or symlink", filepath.Base(path))
		}
		if info.Size() == 0 {
			return unitreeG1Packages{}, fmt.Errorf("required package %s is empty", filepath.Base(path))
		}
	}
	return result, nil
}

func selectUnitreeG1Drive(ctx context.Context, requested string) (drive, error) {
	if requested == "" {
		fmt.Println()
		return pickExternalDrive(ctx)
	}
	drives, err := listAllDrives()
	if err != nil {
		return drive{}, fmt.Errorf("listing drives: %w", err)
	}
	for _, candidate := range drives {
		if candidate.DevicePath == requested {
			return candidate, nil
		}
	}
	return drive{}, fmt.Errorf("replacement drive %s was not found among external drives", requested)
}

func validateUnitreeG1Drive(target drive) error {
	if !target.IsRemovable {
		return fmt.Errorf("%s is not an external drive; attach the replacement NVMe through a USB enclosure", target.DevicePath)
	}
	if !target.MediaFixed {
		return fmt.Errorf("%s does not look like a fixed SSD; select the replacement NVMe enclosure, not an SD card or USB stick", target.DevicePath)
	}
	if target.SizeBytes < unitreeG1MinDriveBytes {
		return fmt.Errorf("%s is %s; the verified G1 procedure requires a replacement NVMe of at least 1 TB", target.DevicePath, target.Size)
	}
	return nil
}

type bzip2ImageReadCloser struct {
	io.Reader
	file *os.File
}

func (r *bzip2ImageReadCloser) Close() error { return r.file.Close() }

func streamBzip2Image(path string) (*imageStream, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening G1 image: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat G1 image: %w", err)
	}
	var magic [3]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil || string(magic[:]) != "BZh" {
		file.Close()
		return nil, fmt.Errorf("%s is not a bzip2 image", filepath.Base(path))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	counter := &countingReader{r: file}
	return &imageStream{
		ReadCloser:     &bzip2ImageReadCloser{Reader: bzip2.NewReader(counter), file: file},
		compressedRead: counter.n.Load,
		compressedSize: info.Size(),
	}, nil
}

func writeUnitreeG1Image(stream *imageStream, target drive) error {
	progress := tui.NewProgress("Writing Unitree G1 image to " + target.DevicePath + "...")
	program := tui.NewProgressProgram(progress)
	go func() {
		err := writeImageToDisk(stream, 0, target, func(written int64) {
			if msg, ok := stream.writeProgressMsg(written); ok {
				program.Send(msg)
			}
		})
		program.Send(tui.ProgressDoneMsg{Err: err})
	}()
	final, err := program.Run()
	if err != nil {
		return fmt.Errorf("progress TUI: %w", err)
	}
	if err := final.(tui.ProgressModel).Err(); err != nil {
		return fmt.Errorf("writing Unitree G1 image: %w", err)
	}
	return nil
}

func pickUnitreeG1RecoveryDevice() (rcm.RecoveryDevice, error) {
	dev, err := pickUnixRecoveryDevice(recoveryWaitHints{
		label:       "Unitree G1 PC2 Orin NX",
		cablingLine: "the data cable is connected to PC2's dedicated USB-C flashing port",
		buttonLine:  "hold PWR + REC 2s, release PWR while holding REC 2s more, then release REC",
	}, func(candidate rcm.RecoveryDevice) bool { return candidate.IsOrinNX() })
	if err != nil {
		return rcm.RecoveryDevice{}, err
	}
	all, scanErr := rcm.ListRecoveryDevices()
	if scanErr != nil {
		return rcm.RecoveryDevice{}, scanErr
	}
	if len(all) != 1 {
		return rcm.RecoveryDevice{}, fmt.Errorf("the Unitree vendor flash script cannot target a USB path safely; disconnect every other Jetson in recovery mode and retry")
	}
	if !all[0].IsOrinNX() || all[0].PathKey != dev.PathKey {
		return rcm.RecoveryDevice{}, fmt.Errorf("the only connected recovery device is not the selected G1 Orin NX")
	}
	return dev, nil
}

func extractUnitreeG1Firmware(ctx context.Context, archive string) (workspace, script string, err error) {
	if err := validateUnitreeG1Archive(archive); err != nil {
		return "", "", err
	}
	workspace, err = os.MkdirTemp("", "wendy-unitree-g1-")
	if err != nil {
		return "", "", err
	}
	cmd := exec.CommandContext(ctx, "tar", "--bzip2", "--extract", "--file", archive, "--directory", workspace, "--no-same-owner", "--no-same-permissions")
	if output, extractErr := cmd.CombinedOutput(); extractErr != nil {
		os.RemoveAll(workspace)
		return "", "", fmt.Errorf("extracting Unitree firmware archive: %w: %s", extractErr, strings.TrimSpace(string(output)))
	}
	script, err = findUnitreeG1FlashScript(workspace)
	if err != nil {
		os.RemoveAll(workspace)
		return "", "", err
	}
	return workspace, script, nil
}

func validateUnitreeG1Archive(archive string) error {
	file, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("opening Unitree firmware archive: %w", err)
	}
	defer file.Close()
	return validateUnitreeG1Tar(bzip2.NewReader(file))
}

func validateUnitreeG1Tar(source io.Reader) error {
	reader := tar.NewReader(source)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading Unitree firmware archive: %w", err)
		}
		if !safeUnitreeG1ArchivePath(header.Name) {
			return fmt.Errorf("Unitree firmware archive contains unsafe path %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeDir:
		case tar.TypeSymlink:
			if path.IsAbs(header.Linkname) {
				return fmt.Errorf("Unitree firmware archive symlink %q has an absolute target", header.Name)
			}
			resolved := path.Join(path.Dir(header.Name), header.Linkname)
			if !safeUnitreeG1ArchivePath(resolved) {
				return fmt.Errorf("Unitree firmware archive symlink %q escapes the extraction folder", header.Name)
			}
		case tar.TypeLink:
			if !safeUnitreeG1ArchivePath(header.Linkname) {
				return fmt.Errorf("Unitree firmware archive hard link %q escapes the extraction folder", header.Name)
			}
		default:
			return fmt.Errorf("Unitree firmware archive contains unsupported entry type %d at %q", header.Typeflag, header.Name)
		}
	}
}

func safeUnitreeG1ArchivePath(name string) bool {
	if name == "" || path.IsAbs(name) {
		return false
	}
	clean := path.Clean(name)
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

func findUnitreeG1FlashScript(root string) (string, error) {
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == "flash_nx_module.sh" && filepath.Base(filepath.Dir(path)) == "Linux_for_Tegra" {
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("flash_nx_module.sh must be a regular file")
			}
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one Linux_for_Tegra/flash_nx_module.sh in Unitree firmware archive, found %d", len(matches))
	}
	return matches[0], nil
}

func runUnitreeG1FirmwareScript(ctx context.Context, script string) error {
	cmd := exec.CommandContext(ctx, "sudo", "./flash_nx_module.sh")
	cmd.Dir = filepath.Dir(script)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Unitree flash_nx_module.sh failed: %w", err)
	}
	return nil
}
