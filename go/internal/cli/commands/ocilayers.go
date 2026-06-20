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

// ociDescriptor is a descriptor entry as it appears in an OCI index.json or a
// nested image-index manifest list.
type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Platform  *struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	} `json:"platform"`
}

func isOCIImageIndexMediaType(mt string) bool {
	return mt == "application/vnd.oci.image.index.v1+json" ||
		mt == "application/vnd.docker.distribution.manifest.list.v2+json"
}

func isOCIImageManifestMediaType(mt string) bool {
	// "" is treated as a leaf image manifest to preserve buildx layouts whose
	// index.json entries omit the mediaType.
	return mt == "" ||
		mt == "application/vnd.oci.image.manifest.v1+json" ||
		mt == "application/vnd.docker.distribution.manifest.v2+json"
}

// parseOCIPlatform splits a "os/arch[/variant]" platform string into its os and
// architecture components. Empty parts are returned when absent.
func parseOCIPlatform(platform string) (os, arch string) {
	parts := strings.Split(platform, "/")
	if len(parts) > 0 {
		os = parts[0]
	}
	if len(parts) > 1 {
		arch = parts[1]
	}
	return os, arch
}

// resolveOCIImageManifest follows OCI descriptors from an index to the concrete
// image manifest blob for the target platform, descending through nested
// image-indexes (Apple Container's `image save` produces one or two levels).
// It returns the raw image-manifest JSON.
func resolveOCIImageManifest(descs []ociDescriptor, blobs map[string][]byte, wantOS, wantArch string, depth int) ([]byte, error) {
	if depth > 4 {
		return nil, fmt.Errorf("OCI index nesting too deep")
	}
	chosen := pickOCIDescriptor(descs, wantOS, wantArch)
	if chosen == nil {
		return nil, fmt.Errorf("no image manifest found in OCI layout")
	}
	hex, err := digestToHex(chosen.Digest)
	if err != nil {
		return nil, fmt.Errorf("invalid manifest digest %q: %w", chosen.Digest, err)
	}
	blob, ok := blobs[hex]
	if !ok {
		return nil, fmt.Errorf("manifest blob %s not found in OCI tar", chosen.Digest)
	}
	if isOCIImageIndexMediaType(chosen.MediaType) {
		var nested struct {
			Manifests []ociDescriptor `json:"manifests"`
		}
		if err := json.Unmarshal(blob, &nested); err != nil {
			return nil, fmt.Errorf("parsing nested image index: %w", err)
		}
		if len(nested.Manifests) == 0 {
			return nil, fmt.Errorf("nested image index has no manifests")
		}
		return resolveOCIImageManifest(nested.Manifests, blobs, wantOS, wantArch, depth+1)
	}
	return blob, nil
}

// pickOCIDescriptor chooses the best descriptor for the target platform:
// an exact os/arch match if present, otherwise the first image manifest or
// image index (skipping attestation/unknown entries).
func pickOCIDescriptor(descs []ociDescriptor, wantOS, wantArch string) *ociDescriptor {
	for i := range descs {
		d := &descs[i]
		if d.Platform != nil && d.Platform.OS == wantOS && d.Platform.Architecture == wantArch {
			return d
		}
	}
	for i := range descs {
		d := &descs[i]
		if isOCIImageManifestMediaType(d.MediaType) || isOCIImageIndexMediaType(d.MediaType) {
			return d
		}
	}
	return nil
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
func readOCILayoutLayers(ociTarPath, platform string) ([]localLayer, []byte, error) {
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

	// Parse index.json and resolve to a concrete image manifest. buildx emits
	// index.json → image-manifest directly, while Apple Container's `image save`
	// wraps the image in one (or two) nested image-indexes; both are handled by
	// following index descriptors to the manifest matching the target platform.
	var index struct {
		Manifests []ociDescriptor `json:"manifests"`
	}
	if err := json.Unmarshal(indexJSON, &index); err != nil {
		return nil, nil, fmt.Errorf("parsing index.json: %w", err)
	}
	if len(index.Manifests) == 0 {
		return nil, nil, fmt.Errorf("index.json has no manifests")
	}

	wantOS, wantArch := parseOCIPlatform(platform)
	manifestData, err := resolveOCIImageManifest(index.Manifests, blobs, wantOS, wantArch, 0)
	if err != nil {
		return nil, nil, err
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

// buildImageToOCILayout builds an OCI-layout tar to dest for the chunk-diff
// deploy path. When builder is apple-container, it uses the Apple Container CLI;
// otherwise it runs `docker buildx build` with `--output type=oci,dest=<dest>`.
// It mirrors the flag/cache/env setup of buildAndPushImage but skips registry
// push entirely.
func buildImageToOCILayout(ctx context.Context, cwd, dockerfile, platform string, buildArgs map[string]string, builder, dest string, stdout, stderr io.Writer) error {
	normalized, err := normalizeImageBuilder(builder)
	if err != nil {
		return err
	}
	if normalized == imageBuilderAppleContainer {
		return buildImageToOCILayoutWithAppleContainer(ctx, cwd, dockerfile, platform, buildArgs, dest, stdout, stderr)
	}

	// Sub-phase timing (gated on WENDY_TIMING) to split the "build (oci export)"
	// phase into lock acquisition, builder verification (the buildx inspect
	// calls), and the actual buildx solve.
	submark := phaseTimer()

	// Re-use the shared buildx builder (without mTLS; we don't push to a registry).
	releaseLock, err := buildLock.acquire(ctx, stderr)
	if err != nil {
		return err
	}
	defer releaseLock()
	submark("  build: acquire lock")

	// Use a dedicated builder for OCI-layout export. It needs no registry
	// config, so it is created once and reused without the per-run
	// config-inject/restart cycle the registry builder pays.
	buildxBuilder, err := ensureOCIExportBuilder(ctx, stderr)
	if err != nil {
		return err
	}
	submark("  build: ensure builder (inspects)")

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
		"--builder", buildxBuilder,
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

	submark("  build: setup (cache/env)")

	fmt.Fprintf(stderr, "[buildx] starting OCI export: docker %s\n", strings.Join(redactBuildArgsForLog(args), " "))
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

// buildImageToOCILayoutWithAppleContainer builds the image with the Apple
// Container CLI and exports it as an OCI-layout tar at dest for the chunk-diff
// deploy path. Apple Container cannot stream an OCI tar straight from `build`
// (its `-o type=oci,dest=` writes inside the build VM and never reaches the
// host), so we build into the local image store under a unique temporary tag,
// `image save` it to the host, and remove the tag afterward. This lets the whole
// fast-path deploy run without Docker on Apple silicon.
//
// The caller is responsible for ensuring the Apple Container system is running
// (see ensureAppleContainerSystemForBuilder). There is no local build-cache
// export equivalent to buildx's --cache-to; Apple Container reuses its own
// build cache across runs.
func buildImageToOCILayoutWithAppleContainer(ctx context.Context, cwd, dockerfile, platform string, buildArgs map[string]string, dest string, stdout, stderr io.Writer) error {
	submark := phaseTimer()

	// Serialize with the buildx OCI path so two builders never run at once.
	releaseLock, err := buildLock.acquire(ctx, stderr)
	if err != nil {
		return err
	}
	defer releaseLock()
	submark("  build: acquire lock")

	buildContext, err := appleContainerBuildContextPath(cwd)
	if err != nil {
		return fmt.Errorf("resolving project path: %w", err)
	}

	// Unique per-build tag: dest is a fresh wendy-oci-* tempdir, so concurrent
	// invocations and watch cycles never collide on the temporary image.
	imageRef := "wendy-oci-build:" + sanitizeAppleContainerTag(filepath.Base(filepath.Dir(dest)))

	args := []string{"build", "--platform", platform, "-t", imageRef}
	if dockerfile != "" {
		resolvedDockerfile, err := appleContainerBuildFilePath(cwd, dockerfile)
		if err != nil {
			return err
		}
		args = append(args, "-f", resolvedDockerfile)
	}
	keys, err := sortedValidatedBuildArgKeys(buildArgs)
	if err != nil {
		return err
	}
	for _, k := range keys {
		args = append(args, "--build-arg", k+"="+buildArgs[k])
	}
	args = append(args, buildContext)
	submark("  build: setup")

	fmt.Fprintf(stderr, "[apple-container] building OCI image: container %s\n", strings.Join(redactBuildArgsForLog(args), " "))
	buildCmd := imageBuilderCommandContext(ctx, "container", args...)
	buildCmd.Dir = buildContext
	buildCmd.Stdout = stdout
	buildCmd.Stderr = stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("container build (OCI layout) failed: %w", err)
	}
	// The image is in the store now — remove the temporary tag once we are done,
	// even if the export below is cancelled.
	defer func() {
		rm := imageBuilderCommandContext(context.Background(), "container", "image", "rm", imageRef)
		_ = rm.Run()
	}()

	saveArgs := []string{"image", "save", imageRef, "--platform", platform, "-o", dest}
	fmt.Fprintf(stderr, "[apple-container] exporting OCI layout: container %s\n", strings.Join(saveArgs, " "))
	saveCmd := imageBuilderCommandContext(ctx, "container", saveArgs...)
	saveCmd.Stdout = stdout
	saveCmd.Stderr = stderr
	if err := saveCmd.Run(); err != nil {
		return fmt.Errorf("container image save (OCI layout) failed: %w", err)
	}
	submark("  build: oci save")
	return nil
}

// sanitizeAppleContainerTag maps an arbitrary string to a valid image tag
// ([a-z0-9._-]); anything else becomes '-'.
func sanitizeAppleContainerTag(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "latest"
	}
	return b.String()
}
