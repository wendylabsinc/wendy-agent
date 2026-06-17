package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// localLayer holds a single image layer as its COMPRESSED blob plus the
// metadata needed to address and decompress it. Decompression is deferred
// (see decompress) so callers that can resolve a layer from the manifest cache
// never pay to decompress it.
type localLayer struct {
	Digest    string // compressed OCI layer blob digest ("sha256:<hex>") — stable cache key
	MediaType string // OCI/Docker layer media type (drives decompression)
	Blob      []byte // compressed layer bytes
}

// decompress returns the raw (uncompressed) tar bytes for the layer.
func (l localLayer) decompress() ([]byte, error) {
	return decompressLayer(l.Blob, l.MediaType)
}

// readOCILayoutLayers opens an OCI-layout tar at ociTarPath, walks the
// index.json → manifest → layer descriptors, decompresses each layer to its
// raw tar (by media type), and returns layers in manifest order with
//
//	DiffID = "sha256:" + hex(sha256(rawTar))
//
// It also returns the raw OCI image config blob (the JSON carrying
// Cmd/Entrypoint/Env/WorkingDir/User) so the agent can preserve the original
// runtime config when assembling the image from chunks.
func readOCILayoutLayers(ociTarPath string) ([]localLayer, []byte, error) {
	f, err := os.Open(ociTarPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open OCI tar: %w", err)
	}
	defer f.Close()

	// First pass: index all blobs by their sha256 hex digest.
	blobs := map[string][]byte{} // hex digest → raw blob bytes
	var indexJSON []byte

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading OCI tar: %w", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, fmt.Errorf("reading blob %q: %w", hdr.Name, err)
		}
		switch {
		case hdr.Name == "index.json":
			indexJSON = data
		case strings.HasPrefix(hdr.Name, "blobs/sha256/"):
			blobHex := strings.TrimPrefix(hdr.Name, "blobs/sha256/")
			blobs[blobHex] = data
		}
	}

	if indexJSON == nil {
		return nil, nil, fmt.Errorf("OCI tar missing index.json")
	}

	// Parse index.json to find the image manifest descriptor.
	var index struct {
		Manifests []struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexJSON, &index); err != nil {
		return nil, nil, fmt.Errorf("parsing index.json: %w", err)
	}
	if len(index.Manifests) == 0 {
		return nil, nil, fmt.Errorf("index.json has no manifests")
	}

	// Pick the first image manifest (skip manifest-list entries without
	// a platform match — the task only requires single-image layouts).
	manifestDigest := ""
	for _, m := range index.Manifests {
		// Accept both OCI and Docker manifest media types.
		mt := m.MediaType
		if mt == "application/vnd.oci.image.manifest.v1+json" ||
			mt == "application/vnd.docker.distribution.manifest.v2+json" ||
			mt == "" {
			manifestDigest = m.Digest
			break
		}
	}
	if manifestDigest == "" {
		return nil, nil, fmt.Errorf("no image manifest found in index.json")
	}

	manifestHex, err := digestToHex(manifestDigest)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid manifest digest %q: %w", manifestDigest, err)
	}
	manifestData, ok := blobs[manifestHex]
	if !ok {
		return nil, nil, fmt.Errorf("manifest blob %s not found in OCI tar", manifestDigest)
	}

	// Parse the manifest to get the config descriptor and layer descriptors.
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, nil, fmt.Errorf("parsing manifest: %w", err)
	}

	// Fetch the image config blob so the runtime config (Cmd/Entrypoint/Env/
	// WorkingDir/User) survives reassembly on the device.
	var imageConfig []byte
	if manifest.Config.Digest != "" {
		configHex, err := digestToHex(manifest.Config.Digest)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid config digest %q: %w", manifest.Config.Digest, err)
		}
		cfg, ok := blobs[configHex]
		if !ok {
			return nil, nil, fmt.Errorf("config blob %s not found in OCI tar", manifest.Config.Digest)
		}
		imageConfig = cfg
	}

	layers := make([]localLayer, 0, len(manifest.Layers))
	for i, desc := range manifest.Layers {
		layerHex, err := digestToHex(desc.Digest)
		if err != nil {
			return nil, nil, fmt.Errorf("layer %d: invalid digest %q: %w", i, desc.Digest, err)
		}
		blobData, ok := blobs[layerHex]
		if !ok {
			return nil, nil, fmt.Errorf("layer %d blob %s not found in OCI tar", i, desc.Digest)
		}
		// Keep the compressed blob; decompression is deferred to pushLayersByChunks
		// so unchanged layers resolved from the manifest cache are never decompressed.
		layers = append(layers, localLayer{Digest: desc.Digest, MediaType: desc.MediaType, Blob: blobData})
	}
	return layers, imageConfig, nil
}

// decompressLayer decompresses blobData according to the OCI/Docker layer
// media type. Returns the raw (uncompressed) tar bytes.
func decompressLayer(blobData []byte, mediaType string) ([]byte, error) {
	switch {
	case mediaType == "application/vnd.oci.image.layer.v1.tar" ||
		mediaType == "application/vnd.docker.image.rootfs.diff.tar":
		// Uncompressed — return as-is.
		return blobData, nil

	case strings.HasSuffix(mediaType, ".tar+gzip") ||
		strings.HasSuffix(mediaType, ".tar.gzip") ||
		mediaType == "application/vnd.docker.image.rootfs.diff.tar.gzip":
		gr, err := gzip.NewReader(bytes.NewReader(blobData))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gr.Close()
		out, err := io.ReadAll(gr)
		if err != nil {
			return nil, fmt.Errorf("gzip read: %w", err)
		}
		return out, nil

	case strings.HasSuffix(mediaType, ".tar+zstd") ||
		strings.HasSuffix(mediaType, ".tar.zstd"):
		return decompressZstd(blobData)

	default:
		return nil, fmt.Errorf("unsupported layer media type: %q", mediaType)
	}
}

// decompressZstd decompresses zstd-compressed data and returns the raw bytes.
func decompressZstd(data []byte) ([]byte, error) {
	dec, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	defer dec.Close()
	out, err := io.ReadAll(dec)
	if err != nil {
		return nil, fmt.Errorf("zstd read: %w", err)
	}
	return out, nil
}

// digestToHex converts a "sha256:<hex>" digest string to the bare hex portion.
func digestToHex(digest string) (string, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) {
		return "", fmt.Errorf("only sha256 digests supported, got %q", digest)
	}
	return strings.TrimPrefix(digest, prefix), nil
}

// buildImageToOCILayout runs `docker buildx build` writing an OCI-layout tar
// to dest via `--output type=oci,dest=<dest>`. It mirrors the flag/cache/env
// setup of buildAndPushImage but skips registry push entirely.
func buildImageToOCILayout(ctx context.Context, cwd, dockerfile, platform string, buildArgs map[string]string, dest string, stdout, stderr io.Writer) error {
	// Re-use the shared buildx builder (without mTLS; we don't push to a registry).
	releaseLock, err := buildLock.acquire(ctx, stderr)
	if err != nil {
		return err
	}
	defer releaseLock()

	// Use a dedicated builder for OCI-layout export. It needs no registry
	// config, so it is created once and reused without the per-run
	// config-inject/restart cycle the registry builder pays.
	builder, err := ensureOCIExportBuilder(ctx, stderr)
	if err != nil {
		return err
	}

	userCache, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("finding user cache directory: %w", err)
	}
	cacheDir := filepath.Join(userCache, "wendy", "buildx")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("finding home directory: %w", err)
	}

	// Mirror the clean DOCKER_CONFIG setup from buildAndPushImage (non-Windows).
	var cleanDockerConfigDir string
	if runtime.GOOS != "windows" {
		origDockerConfig := os.Getenv("DOCKER_CONFIG")
		if origDockerConfig == "" {
			origDockerConfig = filepath.Join(home, ".docker")
		}
		cleanDockerConfigDir = filepath.Join(home, ".cache", "wendy", "docker-config")
		if err := os.MkdirAll(cleanDockerConfigDir, 0o755); err != nil {
			return fmt.Errorf("creating clean docker config directory: %w", err)
		}
		cleanDockerConfigFile := filepath.Join(cleanDockerConfigDir, "config.json")
		if err := os.WriteFile(cleanDockerConfigFile, []byte(`{"auths":{}}`), 0o644); err != nil {
			return fmt.Errorf("writing clean docker config: %w", err)
		}
		for _, subdir := range []string{"buildx", "cli-plugins", "contexts"} {
			dst := filepath.Join(cleanDockerConfigDir, subdir)
			if _, err := os.Lstat(dst); err != nil {
				_ = os.Symlink(filepath.Join(origDockerConfig, subdir), dst)
			}
		}
	}

	// buildkitd inside the Linux VM appends "/index.json" to the cache src/dest,
	// so pass forward-slash paths to avoid mixed-separator warnings on Windows.
	cacheDirSlash := filepath.ToSlash(cacheDir)
	args := []string{
		"buildx", "build",
		"--builder", builder,
		"--platform", platform,
	}
	if dockerfile != "" {
		resolvedDockerfile, err := confinedDockerfilePath(cwd, dockerfile)
		if err != nil {
			return err
		}
		args = append(args, "-f", resolvedDockerfile)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "index.json")); err == nil {
		args = append(args, "--cache-from", "type=local,src="+cacheDirSlash)
	}
	args = append(args, "--cache-to", "type=local,dest="+cacheDirSlash)

	// Sort build-arg keys for reproducible argument order.
	keys := make([]string, 0, len(buildArgs))
	for k := range buildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--build-arg", k+"="+buildArgs[k])
	}

	// OCI-layout export instead of registry push.
	args = append(args,
		"--output", "type=oci,dest="+dest,
		".",
	)

	fmt.Fprintf(stderr, "[buildx] starting OCI export: docker %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = cwd
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if cleanDockerConfigDir != "" {
		baseEnv := make([]string, 0, len(os.Environ()))
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "DOCKER_CONFIG=") {
				baseEnv = append(baseEnv, e)
			}
		}
		cmd.Env = append(baseEnv, "DOCKER_CONFIG="+cleanDockerConfigDir)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker buildx build (OCI export) failed: %w", err)
	}
	return nil
}
