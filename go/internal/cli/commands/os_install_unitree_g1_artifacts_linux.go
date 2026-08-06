//go:build linux

package commands

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"golang.org/x/sys/unix"
)

const unitreeG1OfficialGuide = "https://nvlabs.github.io/GR00T-WholeBodyControl/references/jetpack6.html"

type unitreeG1Artifact struct {
	Name   string
	URL    string
	Size   int64
	SHA256 string
}

// These Google Drive file IDs are the two downloads linked by NVIDIA's
// official G1 JetPack 6 guide. Sizes and SHA-256 values were recorded from the
// complete source files on 2026-08-04. Any upstream replacement must be
// reviewed and deliberately re-pinned here before the installer will use it.
var unitreeG1OfficialArtifacts = []unitreeG1Artifact{
	{
		Name:   unitreeG1ImageName,
		URL:    "https://drive.usercontent.google.com/download?id=1I7gb8L3qqDhNHMDclMcD1GgmcCSlrrhy&export=download&confirm=t",
		Size:   3_973_265_059,
		SHA256: "8902fba85a2fbd05893deec10b43d8116661762a718e479bade1ae685e123b3d",
	},
	{
		Name:   unitreeG1FirmwareName,
		URL:    "https://drive.usercontent.google.com/download?id=1bcED2Vy64fyOWIBxK9ck0iXA_Lo9TOyg&export=download&confirm=t",
		Size:   9_938_108_888,
		SHA256: "1ce5da305006a070aa00593561dd432d2c60fcb69dc4c25cb5ee13dbbd74e2fc",
	},
}

func resolveOfficialUnitreeG1Packages(ctx context.Context) (unitreeG1Packages, error) {
	cacheRoot, err := osCacheDir()
	if err != nil {
		return unitreeG1Packages{}, fmt.Errorf("resolving G1 artifact cache: %w", err)
	}
	cacheDir := filepath.Join(cacheRoot, "unitree-g1", unitreeG1Version)
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return unitreeG1Packages{}, fmt.Errorf("creating G1 artifact cache: %w", err)
	}

	resolved := make(map[string]unitreeG1ResolvedArtifact, len(unitreeG1OfficialArtifacts))
	for _, artifact := range unitreeG1OfficialArtifacts {
		path, err := resolveOfficialUnitreeG1Artifact(ctx, cacheDir, artifact)
		if err != nil {
			return unitreeG1Packages{}, err
		}
		resolved[artifact.Name] = unitreeG1ResolvedArtifact{
			Path:   path,
			SHA256: artifact.SHA256,
		}
	}

	return unitreeG1Packages{
		Image:    resolved[unitreeG1ImageName],
		Firmware: resolved[unitreeG1FirmwareName],
	}, nil
}

func resolveOfficialUnitreeG1Artifact(ctx context.Context, cacheDir string, artifact unitreeG1Artifact) (string, error) {
	if err := validateUnitreeG1ArtifactMetadata(artifact); err != nil {
		return "", err
	}
	finalPath := filepath.Join(cacheDir, artifact.Name)
	if valid, err := verifyCachedUnitreeG1Artifact(finalPath, artifact); err != nil {
		return "", err
	} else if valid {
		fmt.Println(tui.SuccessMessage("Using verified cached " + artifact.Name + "."))
		return finalPath, nil
	}

	partialPath := finalPath + ".partial"
	partialSize, err := unitreeG1PartialSize(partialPath, artifact.Size)
	if err != nil {
		return "", err
	}
	remaining := artifact.Size - partialSize
	if available, ok := diskAvailBytes(cacheDir); ok && available < remaining {
		return "", fmt.Errorf("not enough free space for %s: need %.1f GiB more in %s, only %.1f GiB is available",
			artifact.Name, float64(remaining)/(1<<30), cacheDir, float64(available)/(1<<30))
	}

	if err := downloadOfficialUnitreeG1Artifact(ctx, artifact, partialPath); err != nil {
		return "", err
	}
	fingerprint, err := fingerprintUnitreeG1Artifact(partialPath)
	if err != nil {
		return "", fmt.Errorf("verifying downloaded %s: %w", artifact.Name, err)
	}
	if !strings.EqualFold(fingerprint, artifact.SHA256) {
		_ = os.Remove(partialPath)
		return "", fmt.Errorf("official %s checksum mismatch: got %s, expected %s; the upstream file may have changed, so Wendy refuses to flash it",
			artifact.Name, fingerprint, artifact.SHA256)
	}
	if err := os.Rename(partialPath, finalPath); err != nil {
		return "", fmt.Errorf("caching verified %s: %w", artifact.Name, err)
	}
	return finalPath, nil
}

func validateUnitreeG1ArtifactMetadata(artifact unitreeG1Artifact) error {
	if artifact.Name == "" || filepath.Base(artifact.Name) != artifact.Name || artifact.URL == "" || artifact.Size <= 0 {
		return fmt.Errorf("invalid built-in G1 artifact metadata for %q", artifact.Name)
	}
	digest, err := hex.DecodeString(artifact.SHA256)
	if err != nil || len(digest) != 32 {
		return fmt.Errorf("invalid built-in SHA-256 for %s", artifact.Name)
	}
	return nil
}

func verifyCachedUnitreeG1Artifact(filePath string, artifact unitreeG1Artifact) (bool, error) {
	info, err := os.Lstat(filePath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspecting cached %s: %w", artifact.Name, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("cached %s must be a regular file, not a directory or symlink", artifact.Name)
	}
	if info.Size() != artifact.Size {
		if err := os.Remove(filePath); err != nil {
			return false, fmt.Errorf("discarding incomplete cached %s: %w", artifact.Name, err)
		}
		return false, nil
	}
	fingerprint, err := fingerprintUnitreeG1Artifact(filePath)
	if err != nil {
		return false, fmt.Errorf("verifying cached %s: %w", artifact.Name, err)
	}
	if strings.EqualFold(fingerprint, artifact.SHA256) {
		return true, nil
	}
	if err := os.Remove(filePath); err != nil {
		return false, fmt.Errorf("discarding checksum-mismatched cached %s: %w", artifact.Name, err)
	}
	return false, nil
}

func unitreeG1PartialSize(filePath string, expected int64) (int64, error) {
	info, err := os.Lstat(filePath)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("partial G1 artifact %s must be a regular file", filePath)
	}
	if info.Size() > expected {
		if err := os.Remove(filePath); err != nil {
			return 0, fmt.Errorf("discarding oversized partial G1 artifact: %w", err)
		}
		return 0, nil
	}
	return info.Size(), nil
}

func downloadOfficialUnitreeG1Artifact(ctx context.Context, artifact unitreeG1Artifact, partialPath string) error {
	downloadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	progress := tui.NewProgress("Downloading official " + artifact.Name + "...")
	program := tui.NewProgressProgram(progress)
	sendProgress := throttledProgress(program, 33*time.Millisecond)
	resultCh := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 6 * time.Hour}
		var err error
		for attempt := 1; attempt <= 3; attempt++ {
			err = resumeUnitreeG1Artifact(downloadCtx, client, artifact, partialPath, sendProgress)
			if err == nil || downloadCtx.Err() != nil || attempt == 3 {
				break
			}
			select {
			case <-downloadCtx.Done():
				err = downloadCtx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		resultCh <- err
		program.Send(tui.ProgressDoneMsg{Err: err})
	}()
	final, runErr := program.Run()
	if runErr != nil {
		cancel()
	}
	resultErr := <-resultCh
	if runErr != nil {
		return fmt.Errorf("download progress TUI: %w", runErr)
	}
	if err := final.(tui.ProgressModel).Err(); err != nil {
		return err
	}
	return resultErr
}

func resumeUnitreeG1Artifact(ctx context.Context, client *http.Client, artifact unitreeG1Artifact, partialPath string, onProgress func(downloaded, total int64)) error {
	fd, err := unix.Open(partialPath, unix.O_WRONLY|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("opening partial %s: %w", artifact.Name, err)
	}
	file := os.NewFile(uintptr(fd), partialPath)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("opening partial %s", artifact.Name)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	offset := info.Size()
	if offset > artifact.Size {
		return fmt.Errorf("partial %s is larger than the expected official artifact", artifact.Name)
	}
	if onProgress != nil {
		onProgress(offset, artifact.Size)
	}
	if offset == artifact.Size {
		return nil
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", artifact.Name, err)
	}
	defer response.Body.Close()

	if offset > 0 && response.StatusCode == http.StatusOK {
		if err := file.Truncate(0); err != nil {
			return err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		offset = 0
	} else if offset > 0 {
		if response.StatusCode != http.StatusPartialContent {
			return fmt.Errorf("resuming %s: expected HTTP 206, got %d", artifact.Name, response.StatusCode)
		}
		wantPrefix := fmt.Sprintf("bytes %d-", offset)
		if !strings.HasPrefix(response.Header.Get("Content-Range"), wantPrefix) {
			return fmt.Errorf("resuming %s: invalid Content-Range %q", artifact.Name, response.Header.Get("Content-Range"))
		}
	} else if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("downloading %s: HTTP %d", artifact.Name, response.StatusCode)
	}

	remaining := artifact.Size - offset
	if response.ContentLength >= 0 && response.ContentLength != remaining {
		return fmt.Errorf("downloading %s: server reports %d remaining bytes, expected %d", artifact.Name, response.ContentLength, remaining)
	}
	written := offset
	buffer := make([]byte, 4<<20)
	for written < artifact.Size {
		limit := artifact.Size - written
		if int64(len(buffer)) > limit {
			buffer = buffer[:limit]
		}
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if _, err := file.Write(buffer[:n]); err != nil {
				return fmt.Errorf("caching %s: %w", artifact.Name, err)
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, artifact.Size)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", artifact.Name, readErr)
		}
	}
	if written != artifact.Size {
		return fmt.Errorf("downloaded %s is incomplete: got %d bytes, expected %d", artifact.Name, written, artifact.Size)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", artifact.Name, err)
	}
	return nil
}
