//go:build linux

package commands

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/rcm"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

const (
	unitreeG1Version           = "6.2"
	unitreeG1ImageName         = "g1-nx-j6.2.img.bz2"
	unitreeG1FirmwareName      = "Jetpack_6.2_nx.tar.bz2"
	unitreeG1MinDriveBytes     = int64(900_000_000_000)
	unitreeG1HistoricalSource  = "https://drive.google.com/drive/folders/1ho17ectOxi7FbaRFdpAbP4tet8BJWjbm"
	unitreeG1TrustPhrase       = "UNVERIFIED UNITREE LAB FLASH"
	unitreeG1MaxArchiveEntries = 1_000_000
	unitreeG1MaxExtractedSize  = int64(200 << 30)
)

type unitreeG1Packages struct {
	Directory string
	Image     string
	Firmware  string
}

type unitreeG1Fingerprints struct {
	Image    string
	Firmware string
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
	fingerprints, err := fingerprintUnitreeG1Packages(packages)
	if err != nil {
		return err
	}
	if err := acceptUnverifiedUnitreeG1Artifacts(fingerprints); err != nil {
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
	fmt.Printf("          sha256:%s\n", fingerprints.Image)
	fmt.Printf("  PC2:    %s\n", filepath.Base(packages.Firmware))
	fmt.Printf("          sha256:%s\n", fingerprints.Firmware)

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
	if err := recheckUnitreeG1Artifact(packages.Image, fingerprints.Image, "before the destructive write"); err != nil {
		return err
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
	if err := recheckUnitreeG1Artifact(packages.Firmware, fingerprints.Firmware, "before extraction and privileged execution"); err != nil {
		return err
	}

	workspace, script, err := extractUnitreeG1Firmware(ctx, packages.Firmware)
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	scriptHash, err := hashUnitreeG1File(script, nil)
	if err != nil {
		return fmt.Errorf("fingerprinting extracted flash script: %w", err)
	}

	fmt.Println()
	fmt.Println(tui.Header("Flashing matching Unitree PC2 module firmware"))
	fmt.Printf("  Archive:   sha256:%s\n", fingerprints.Firmware)
	fmt.Printf("  Script:    %s\n", strings.TrimPrefix(script, workspace+string(filepath.Separator)))
	fmt.Printf("  Script:    sha256:%s\n", scriptHash)
	fmt.Printf("  Directory: %s\n", filepath.Dir(script))
	fmt.Println("  Command:   sudo ./flash_nx_module.sh")
	fmt.Println(tui.WarningMessage("Do not power off the G1 or disconnect USB until flash_nx_module.sh reports success."))
	confirmed, err := tui.Confirm("Run this unverified vendor script with root privileges?")
	if err != nil {
		return err
	}
	if !confirmed {
		return ErrUserCancelled
	}
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

func fingerprintUnitreeG1Packages(packages unitreeG1Packages) (unitreeG1Fingerprints, error) {
	fmt.Println()
	fmt.Println(tui.Header("Fingerprinting selected Unitree artifacts"))
	image, err := fingerprintUnitreeG1Artifact(packages.Image)
	if err != nil {
		return unitreeG1Fingerprints{}, err
	}
	firmware, err := fingerprintUnitreeG1Artifact(packages.Firmware)
	if err != nil {
		return unitreeG1Fingerprints{}, err
	}
	return unitreeG1Fingerprints{Image: image, Firmware: firmware}, nil
}

type unitreeG1HashResult struct {
	digest string
	err    error
}

func fingerprintUnitreeG1Artifact(filePath string) (string, error) {
	progress := tui.NewProgress("Calculating SHA-256 for " + filepath.Base(filePath) + "...")
	program := tui.NewProgressProgram(progress)
	sendProgress := throttledProgress(program, 33*time.Millisecond)
	resultCh := make(chan unitreeG1HashResult, 1)
	go func() {
		digest, err := hashUnitreeG1File(filePath, sendProgress)
		resultCh <- unitreeG1HashResult{digest: digest, err: err}
		program.Send(tui.ProgressDoneMsg{Err: err})
	}()
	final, runErr := program.Run()
	result := <-resultCh
	if runErr != nil {
		return "", fmt.Errorf("fingerprint progress TUI: %w", runErr)
	}
	if err := final.(tui.ProgressModel).Err(); err != nil {
		return "", err
	}
	return result.digest, result.err
}

func hashUnitreeG1File(filePath string, progress func(read, total int64)) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	buffer := make([]byte, 4<<20)
	var read int64
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := hash.Write(buffer[:n]); err != nil {
				return "", err
			}
			read += int64(n)
			if progress != nil {
				progress(read, info.Size())
			}
		}
		if errors.Is(readErr, io.EOF) {
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if readErr != nil {
			return "", readErr
		}
	}
}

func acceptUnverifiedUnitreeG1Artifacts(fingerprints unitreeG1Fingerprints) error {
	fmt.Println()
	fmt.Println(tui.Header("Artifact trust review"))
	fmt.Println(tui.WarningMessage("Wendy has no trusted manifest checksums or signatures for these historical Unitree files."))
	fmt.Println("The hashes below identify the exact selected bytes for this lab run; they do not prove who published them or that they are safe.")
	fmt.Printf("  %s\n    sha256:%s\n", unitreeG1ImageName, fingerprints.Image)
	fmt.Printf("  %s\n    sha256:%s\n", unitreeG1FirmwareName, fingerprints.Firmware)
	fmt.Println("The firmware archive contains a vendor script that will be shown again and executed with sudo after constrained extraction.")
	fmt.Println("Production installation remains blocked until Wendy publishes authenticated artifacts and pins both hashes in its manifest.")
	fmt.Println()
	_, err := tui.PromptText(
		"Type "+unitreeG1TrustPhrase+" to continue",
		"lab-only acceptance; --force does not bypass this",
		validateUnitreeG1TrustPhrase,
	)
	if errors.Is(err, tui.ErrCancelled) {
		return ErrUserCancelled
	}
	return err
}

func validateUnitreeG1TrustPhrase(value string) error {
	if value != unitreeG1TrustPhrase {
		return fmt.Errorf("type %s exactly", unitreeG1TrustPhrase)
	}
	return nil
}

func recheckUnitreeG1Artifact(filePath, acceptedDigest, purpose string) error {
	fmt.Println()
	fmt.Println(tui.Header("Rechecking accepted artifact " + purpose))
	actualDigest, err := fingerprintUnitreeG1Artifact(filePath)
	if err != nil {
		return err
	}
	if actualDigest != acceptedDigest {
		return fmt.Errorf("%s changed after artifact trust acceptance: accepted sha256:%s, current sha256:%s", filepath.Base(filePath), acceptedDigest, actualDigest)
	}
	return nil
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
	workspace, err = os.MkdirTemp("", "wendy-unitree-g1-")
	if err != nil {
		return "", "", err
	}
	if extractErr := extractUnitreeG1Archive(ctx, archive, workspace); extractErr != nil {
		os.RemoveAll(workspace)
		return "", "", extractErr
	}
	script, err = findUnitreeG1FlashScript(workspace)
	if err != nil {
		os.RemoveAll(workspace)
		return "", "", err
	}
	return workspace, script, nil
}

type unitreeG1ArchiveLink struct {
	name   string
	target string
}

type unitreeG1ArchiveDirectory struct {
	path string
	mode os.FileMode
}

func extractUnitreeG1Archive(ctx context.Context, archive, root string) error {
	file, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("opening Unitree firmware archive: %w", err)
	}
	defer file.Close()
	if err := extractUnitreeG1Tar(ctx, bzip2.NewReader(file), root); err != nil {
		return fmt.Errorf("extracting Unitree firmware archive: %w", err)
	}
	return nil
}

func extractUnitreeG1Tar(ctx context.Context, source io.Reader, root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("archive extraction root %q must be a real directory", root)
	}

	reader := tar.NewReader(source)
	seen := make(map[string]byte)
	var hardLinks []unitreeG1ArchiveLink
	var symbolicLinks []unitreeG1ArchiveLink
	var directories []unitreeG1ArchiveDirectory
	var extractedBytes int64
	entries := 0

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading Unitree firmware archive: %w", err)
		}
		entries++
		if entries > unitreeG1MaxArchiveEntries {
			return fmt.Errorf("archive exceeds the %d-entry safety limit", unitreeG1MaxArchiveEntries)
		}

		cleanName, destination, err := unitreeG1ArchiveDestination(root, header.Name)
		if err != nil {
			return err
		}
		if ancestor, ok := unitreeG1SymlinkAncestor(cleanName, seen); ok {
			return fmt.Errorf("archive entry %q would write through symlink %q", header.Name, ancestor)
		}

		var kind byte
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			kind = 'f'
		case tar.TypeDir:
			kind = 'd'
		case tar.TypeSymlink:
			kind = 's'
		case tar.TypeLink:
			kind = 'h'
		default:
			return fmt.Errorf("archive contains unsupported entry type %d at %q", header.Typeflag, header.Name)
		}
		if previous, ok := seen[cleanName]; ok {
			if kind == 'd' && previous == 'd' {
				continue
			}
			return fmt.Errorf("archive contains duplicate path %q", header.Name)
		}
		seen[cleanName] = kind

		switch header.Typeflag {
		case tar.TypeDir:
			if err := makeUnitreeG1ArchiveDirs(root, destination); err != nil {
				return err
			}
			if destination != root {
				directories = append(directories, unitreeG1ArchiveDirectory{
					path: destination,
					mode: os.FileMode(header.Mode) & 0o777,
				})
			}
		case tar.TypeReg, tar.TypeRegA:
			if destination == root {
				return fmt.Errorf("archive regular file %q resolves to the extraction root", header.Name)
			}
			if header.Size < 0 || header.Size > unitreeG1MaxExtractedSize-extractedBytes {
				return fmt.Errorf("archive exceeds the %d GiB extracted-size safety limit", unitreeG1MaxExtractedSize>>30)
			}
			if err := makeUnitreeG1ArchiveDirs(root, filepath.Dir(destination)); err != nil {
				return err
			}
			output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return fmt.Errorf("creating archive file %q: %w", header.Name, err)
			}
			written, copyErr := io.Copy(output, &unitreeG1ContextReader{ctx: ctx, reader: reader})
			closeErr := output.Close()
			if copyErr != nil {
				return fmt.Errorf("extracting archive file %q: %w", header.Name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("closing archive file %q: %w", header.Name, closeErr)
			}
			if written != header.Size {
				return fmt.Errorf("archive file %q declared %d bytes but produced %d", header.Name, header.Size, written)
			}
			extractedBytes += written
			if err := os.Chmod(destination, os.FileMode(header.Mode)&0o777); err != nil {
				return fmt.Errorf("setting archive file mode for %q: %w", header.Name, err)
			}
		case tar.TypeSymlink:
			if path.IsAbs(header.Linkname) {
				return fmt.Errorf("archive symlink %q has an absolute target", header.Name)
			}
			resolved := path.Join(path.Dir(header.Name), header.Linkname)
			if !safeUnitreeG1ArchivePath(resolved) {
				return fmt.Errorf("archive symlink %q escapes the extraction folder", header.Name)
			}
			if err := makeUnitreeG1ArchiveDirs(root, filepath.Dir(destination)); err != nil {
				return err
			}
			symbolicLinks = append(symbolicLinks, unitreeG1ArchiveLink{name: destination, target: header.Linkname})
		case tar.TypeLink:
			cleanTarget, target, err := unitreeG1ArchiveDestination(root, header.Linkname)
			if err != nil {
				return fmt.Errorf("archive hard link %q: %w", header.Name, err)
			}
			if seen[cleanTarget] != 'f' {
				return fmt.Errorf("archive hard link %q must target a prior regular file", header.Name)
			}
			if err := makeUnitreeG1ArchiveDirs(root, filepath.Dir(destination)); err != nil {
				return err
			}
			hardLinks = append(hardLinks, unitreeG1ArchiveLink{name: destination, target: target})
		}
	}

	// Regular files are fully materialized before any links are created. This
	// prevents a later archive entry from writing through an extracted symlink.
	for _, link := range hardLinks {
		info, err := os.Lstat(link.target)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("archive hard link target %q is not a regular extracted file", link.target)
		}
		if err := os.Link(link.target, link.name); err != nil {
			return fmt.Errorf("creating archive hard link %q: %w", link.name, err)
		}
	}
	for _, link := range symbolicLinks {
		if _, err := os.Lstat(link.name); err == nil || !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("archive symlink destination %q already exists", link.name)
		}
		if err := os.Symlink(link.target, link.name); err != nil {
			return fmt.Errorf("creating archive symlink %q: %w", link.name, err)
		}
	}

	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i].path, string(filepath.Separator)) > strings.Count(directories[j].path, string(filepath.Separator))
	})
	for _, directory := range directories {
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			return fmt.Errorf("setting archive directory mode for %q: %w", directory.path, err)
		}
	}
	return nil
}

type unitreeG1ContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *unitreeG1ContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func unitreeG1ArchiveDestination(root, name string) (cleanName, destination string, err error) {
	if !safeUnitreeG1ArchivePath(name) {
		return "", "", fmt.Errorf("archive contains unsafe path %q", name)
	}
	cleanName = path.Clean(name)
	destination = filepath.Join(root, filepath.FromSlash(cleanName))
	relative, err := filepath.Rel(root, destination)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("archive path %q escapes the extraction folder", name)
	}
	return cleanName, destination, nil
}

func unitreeG1SymlinkAncestor(cleanName string, seen map[string]byte) (string, bool) {
	for parent := path.Dir(cleanName); parent != "." && parent != "/"; parent = path.Dir(parent) {
		if seen[parent] == 's' {
			return parent, true
		}
	}
	return "", false
}

func makeUnitreeG1ArchiveDirs(root, directory string) error {
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("archive directory %q escapes the extraction folder", directory)
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(current, 0o700); err != nil {
				return fmt.Errorf("creating archive directory %q: %w", current, err)
			}
		case err != nil:
			return err
		case !info.IsDir():
			return fmt.Errorf("archive path component %q is not a real directory", current)
		}
	}
	return nil
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
	// SECURITY: This draft-only lab path intentionally runs operator-selected,
	// unverified Unitree firmware as root because no authenticated publisher
	// artifacts exist yet. The UI fingerprints both artifacts, requires typed
	// risk acceptance, extracts without following archive links, shows the exact
	// script hash/working directory/command, and requires a second confirmation.
	// Production enablement must replace this exception with Wendy-controlled,
	// signed or manifest-pinned artifacts; a locally calculated hash alone does
	// not establish authenticity.
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
