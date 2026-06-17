package commands

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// downloadBmap fetches a (small) .bmap XML file to dst. The bmap is tiny, so a
// single GET with a short timeout is sufficient. Any failure is the caller's
// cue to fall back to dd. The download is atomic: data is written to a
// temporary file and renamed into place only on success, so an interrupted
// download never leaves a corrupt file for a later run to parse.
func downloadBmap(url, dst string) error {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("fetching bmap: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bmap returned status %d", resp.StatusCode)
	}

	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating bmap file: %w", err)
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 64<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("writing bmap file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("closing bmap file: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("finalizing bmap file: %w", err)
	}
	return nil
}

// countingWriter wraps an io.Writer and reports cumulative bytes written via
// progressFn. Used to drive the flash progress bar from bytes fed to the helper.
type countingWriter struct {
	w          io.Writer
	n          int64
	progressFn func(int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	written, err := c.w.Write(p)
	c.n += int64(written)
	if c.progressFn != nil {
		c.progressFn(c.n)
	}
	return written, err
}

// validateBmapSource rejects a --source that is not beneath the OS image cache
// root. The helper runs as root, so this prevents a caller from pointing it at
// an arbitrary path via sudo.
func validateBmapSource(path string) error {
	root, err := osCacheDir()
	if err != nil {
		return fmt.Errorf("resolving cache root: %w", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	if abs != absRoot && !strings.HasPrefix(abs, absRoot+string(filepath.Separator)) {
		return fmt.Errorf("source %s is outside the image cache", path)
	}
	return nil
}

// validateDeviceTarget rejects a --device that is not a block/character device.
func validateDeviceTarget(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat device: %w", err)
	}
	if info.Mode()&os.ModeDevice == 0 {
		return fmt.Errorf("%s is not a device", path)
	}
	return nil
}

// runBmapWriteSeekable is the body of `__bmap-write` when --source is given. It
// opens the seekable image and writes only mapped ranges to the raw device,
// emitting cumulative bytes written on stdout (one decimal per line) so the
// parent can drive the progress bar. Runs as root; no stdin pipe. writers > 0
// overrides the writer concurrency (the parent picks it from the storage type);
// 0 keeps the sequential default that is correct for SD/USB media.
func runBmapWriteSeekable(devicePath, bmapPath, sourcePath string, writers int, stdout io.Writer) (retErr error) {
	if writers > 0 {
		bmapWriteConcurrency = writers
	}
	if err := validateDeviceTarget(devicePath); err != nil {
		return err
	}
	if err := validateBmapSource(sourcePath); err != nil {
		return err
	}
	data, err := os.ReadFile(bmapPath)
	if err != nil {
		return fmt.Errorf("reading bmap: %w", err)
	}
	b, err := parseBmap(data)
	if err != nil {
		return err
	}
	si, err := openSeekableZstd(sourcePath)
	if err != nil {
		return err
	}
	defer si.Close()
	if si.Size() != b.ImageSize {
		return fmt.Errorf("seekable image size %d != bmap image size %d", si.Size(), b.ImageSize)
	}
	dev, err := os.OpenFile(devicePath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("opening device %s: %w", devicePath, err)
	}
	defer func() {
		if cerr := dev.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("closing device %s: %w", devicePath, cerr)
		}
	}()
	emit := func(n int64) { fmt.Fprintf(stdout, "%d\n", n) }
	if err := applyBmapSeekable(si, dev, b, emit); err != nil {
		return err
	}
	if err := dev.Sync(); err != nil && !errors.Is(err, syscall.ENOTTY) {
		return fmt.Errorf("syncing device %s: %w", devicePath, err)
	}
	return nil
}

// scanBmapProgress reads the helper's stdout (one cumulative decimal byte count
// per line) and forwards each value to progressFn. Used by the parent process
// in writeImageWithBmapSeekable (added in a later task).
func scanBmapProgress(r io.Reader, progressFn func(int64)) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if n, err := strconv.ParseInt(line, 10, 64); err == nil {
			progressFn(n)
		}
	}
}

// runBmapWrite is the body of the hidden __bmap-write helper subcommand
// (Linux/macOS). It opens the raw device, reads the decompressed image from
// the supplied reader (stdin in production), and applies the bmap. It runs as
// root (re-exec'd under sudo by the parent), so it does not prompt or print
// TUI — progress is tracked by the parent counting bytes fed to stdin.
func runBmapWrite(devicePath, bmapPath string, image io.Reader) (retErr error) {
	data, err := os.ReadFile(bmapPath)
	if err != nil {
		return fmt.Errorf("reading bmap: %w", err)
	}
	b, err := parseBmap(data)
	if err != nil {
		return err
	}
	dev, err := os.OpenFile(devicePath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("opening device %s: %w", devicePath, err)
	}
	defer func() {
		if closeErr := dev.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("closing device %s: %w", devicePath, closeErr)
		}
	}()
	if err := applyBmap(image, dev, b, func(int64) {}); err != nil {
		return err
	}
	// Flush to media. fsync works on Linux block devices, but macOS raw
	// character devices (/dev/rdiskN) reject it with ENOTTY — there the parent
	// runs sync(8) after we exit, so a missing fsync is harmless. Treat ENOTTY
	// as success rather than failing a flash whose data already landed.
	if err := dev.Sync(); err != nil && !errors.Is(err, syscall.ENOTTY) {
		return fmt.Errorf("syncing device %s: %w", devicePath, err)
	}
	return nil
}
