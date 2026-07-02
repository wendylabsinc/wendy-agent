//go:build darwin || linux || windows

package commands

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

// blobKind classifies a user-supplied blob for `wendy os install <blob>`.
type blobKind int

const (
	// blobDiskImage is a raw/compressed disk image written to a drive.
	blobDiskImage blobKind = iota
	// blobFlashpack is a Thor flashpack tarball flashed over USB recovery.
	blobFlashpack
)

// blobInstallOptions carries the flags applicable to the one-positional blob
// install mode.
type blobInstallOptions struct {
	drive                string
	force                bool
	yesOverwriteInternal bool
	wifi                 wifiCLIOptions
	deviceName           string
	preOpts              preEnrollOptions
}

// flashpackIncompatibleFlags names the flags that make no sense for a
// flashpack blob: a Thor flashes over USB recovery, not to a drive, and no
// config partition is written during the flash.
func (o blobInstallOptions) flashpackIncompatibleFlags() []string {
	var set []string
	if o.drive != "" {
		set = append(set, "--drive")
	}
	if o.wifi.SSID != "" {
		set = append(set, "--wifi-ssid")
	}
	if o.wifi.Password != "" {
		set = append(set, "--wifi-password")
	}
	if len(o.wifi.Entries) > 0 {
		set = append(set, "--wifi")
	}
	if o.wifi.NoWifi {
		set = append(set, "--no-wifi")
	}
	if o.deviceName != "" {
		set = append(set, "--device-name")
	}
	if o.preOpts.mode == preEnrollForced {
		set = append(set, "--pre-enroll")
	}
	if o.preOpts.cloudGRPC != "" {
		set = append(set, "--cloud-grpc")
	}
	return set
}

// isRemoteBlobURL reports whether arg names a blob by http(s) URL rather than
// a local path.
func isRemoteBlobURL(arg string) bool {
	return strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://")
}

// runOSInstallBlob installs a custom blob — a disk image or a Thor flashpack,
// from a local path or an http(s) URL. The blob's type decides the flow: disk
// images are written to a drive (interactive picker unless --drive), flashpacks
// route to the Thor USB-recovery flash.
func runOSInstallBlob(ctx context.Context, arg string, opts blobInstallOptions) error {
	localPath, nameHint := arg, arg
	if isRemoteBlobURL(arg) {
		u, err := url.Parse(arg)
		if err != nil {
			return fmt.Errorf("invalid blob URL: %w", err)
		}
		nameHint = path.Base(u.Path)
		fmt.Printf("Downloading %s...\n", arg)
		tmp, err := downloadImage(&imageInfo{DownloadURL: arg, Version: nameHint})
		if err != nil {
			return fmt.Errorf("downloading blob: %w", err)
		}
		defer os.Remove(tmp)
		localPath = tmp
	} else if _, err := os.Stat(arg); err != nil {
		return fmt.Errorf("image file: %w", err)
	}

	kind, err := detectBlobKind(localPath, nameHint)
	if err != nil {
		return err
	}

	switch kind {
	case blobFlashpack:
		if bad := opts.flashpackIncompatibleFlags(); len(bad) != 0 {
			return fmt.Errorf("%s is a Thor flashpack, which flashes over USB recovery: %s cannot be used with it", nameHint, strings.Join(bad, ", "))
		}
		return installThorLocalFlashpack(ctx, localPath, nameHint, opts.force)
	default:
		return installLocalDiskImage(ctx, localPath, opts)
	}
}

// installLocalDiskImage writes a local disk-image blob to a drive: elevation
// pre-auth → drive resolve (--drive or interactive picker) → destructive-write
// confirm → provisioning inputs → stream write → config-partition provisioning.
// The mirror of installLinuxImage without the manifest/download/bmap steps.
func installLocalDiskImage(ctx context.Context, imagePath string, opts blobInstallOptions) error {
	if err := preAuthElevation(); err != nil {
		return err
	}
	elevationCtx, cancelElevation := context.WithCancel(ctx)
	defer cancelElevation()
	keepElevationAlive(elevationCtx)

	var targetDrive drive
	if opts.drive != "" {
		d, err := findDriveByPath(opts.drive)
		if err != nil {
			return err
		}
		targetDrive = d
	} else {
		fmt.Println()
		d, err := pickExternalDrive(ctx)
		if err != nil {
			return err
		}
		targetDrive = d
	}

	if err := confirmOverwriteInternalDrive(targetDrive, opts.force, opts.yesOverwriteInternal); err != nil {
		return err
	}
	if !opts.force {
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

	provCreds, provDeviceName, provisioningJSON, err := resolveProvisioningInputs(ctx, opts.wifi, opts.deviceName, opts.preOpts)
	if err != nil {
		return err
	}

	stream, err := openLocalImageStream(imagePath)
	if err != nil {
		return fmt.Errorf("opening image: %w", err)
	}
	defer stream.Close()

	if stream.uncompressedSize == 0 && stream.sourcePath != "" {
		if err := measureImageWithProgress(stream); err != nil {
			fmt.Printf("Could not determine image size: %v\n", err)
		}
	}

	fmt.Printf("Writing image to %s...\n", targetDrive.DevicePath)
	fmt.Println(elevationHint())
	wp := tui.NewProgressProgram(tui.NewProgress(fmt.Sprintf("Writing to %s...", targetDrive.DevicePath)))
	go func() {
		wp.Send(tui.ProgressDoneMsg{Err: writeImageToDisk(stream, stream.uncompressedSize, targetDrive, func(written int64) {
			if msg, ok := stream.writeProgressMsg(written); ok {
				wp.Send(msg)
			}
		})})
	}()
	final, err := wp.Run()
	if err != nil {
		return fmt.Errorf("progress TUI: %w", err)
	}
	if m := final.(tui.ProgressModel); m.Err() != nil {
		return fmt.Errorf("writing image: %w", m.Err())
	}

	if err := applyProvisioningAndEject(targetDrive, provCreds, provDeviceName, provisioningJSON); err != nil {
		return err
	}

	fmt.Printf("\nSuccessfully installed image on %s.\n", targetDrive.Name)
	fmt.Println("You can now insert the drive into your device and power it on.")
	return nil
}

// detectBlobKind classifies a local blob file. nameHint is the name the user
// supplied (a URL's path basename for downloads) and is consulted first; when
// the extension is unknown or ambiguous the file's magic bytes decide.
func detectBlobKind(filePath, nameHint string) (blobKind, error) {
	name := strings.ToLower(path.Base(strings.ReplaceAll(nameHint, "\\", "/")))
	switch {
	case strings.HasSuffix(name, ".tegraflash"),
		strings.HasSuffix(name, ".flashpack"),
		strings.HasSuffix(name, ".tar.zst"):
		return blobFlashpack, nil
	case strings.HasSuffix(name, ".img"),
		strings.HasSuffix(name, ".raw"),
		strings.HasSuffix(name, ".wic"),
		strings.HasSuffix(name, ".sdimg"),
		strings.HasSuffix(name, ".zip"),
		strings.HasSuffix(name, ".gz"),
		strings.HasSuffix(name, ".img.zst"):
		return blobDiskImage, nil
	}
	return sniffBlobKind(filePath)
}

// sniffBlobKind classifies a blob by content. gzip and zip are always disk
// images; zstd is ambiguous (a flashpack .tar.zst vs an .img.zst), so the
// first decompressed block is checked for the tar magic. Anything else is
// treated as a raw disk image.
func sniffBlobKind(filePath string) (blobKind, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return blobDiskImage, fmt.Errorf("image file: %w", err)
	}
	defer f.Close()

	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return blobDiskImage, nil // too short to sniff; let the image path report it
	}
	switch {
	case magic[0] == 0x1f && magic[1] == 0x8b: // gzip
		return blobDiskImage, nil
	case magic[0] == 'P' && magic[1] == 'K': // zip
		return blobDiskImage, nil
	case magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd: // zstd
		isTar, err := zstdContainsTar(f)
		if err != nil {
			return blobDiskImage, err
		}
		if isTar {
			return blobFlashpack, nil
		}
		return blobDiskImage, nil
	default:
		return blobDiskImage, nil
	}
}

// isZstdFile returns true when path begins with the zstd magic bytes.
func isZstdFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	_, err = io.ReadFull(f, magic[:])
	return err == nil && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd
}

// zstdReadCloser wraps a zstd.Decoder so that closing it also closes the
// underlying file, matching the io.ReadCloser contract.
type zstdReadCloser struct {
	dec *zstd.Decoder
	f   *os.File
}

func (z *zstdReadCloser) Read(p []byte) (int, error) { return z.dec.Read(p) }
func (z *zstdReadCloser) Close() error {
	z.dec.Close()
	return z.f.Close()
}

// streamZstdImage opens a zstd-compressed image file for sequential writing.
// A seekable-zstd file carries the exact decompressed size in its seek table,
// so try that layout first; a plain zstd stream can't report its size and
// falls back to compressed-consumption progress.
func streamZstdImage(path string) (*imageStream, error) {
	if si, err := openSeekableZstd(path); err == nil {
		return &imageStream{ReadCloser: si, uncompressedSize: si.Size()}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening zstd image: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat zstd image: %w", err)
	}
	cr := &countingReader{r: f}
	dec, err := zstd.NewReader(cr)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("creating zstd reader: %w", err)
	}
	return &imageStream{
		ReadCloser:     &zstdReadCloser{dec: dec, f: f},
		compressedRead: cr.n.Load,
		compressedSize: info.Size(),
	}, nil
}

// zstdContainsTar decompresses the first tar header block of a zstd file and
// checks for the "ustar" magic at offset 257.
func zstdContainsTar(f *os.File) (bool, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	dec, err := zstd.NewReader(f)
	if err != nil {
		return false, fmt.Errorf("reading zstd blob: %w", err)
	}
	defer dec.Close()
	buf := make([]byte, 262)
	n, err := io.ReadFull(dec, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return false, fmt.Errorf("decompressing zstd blob: %w", err)
	}
	return n >= 262 && string(buf[257:262]) == "ustar", nil
}
