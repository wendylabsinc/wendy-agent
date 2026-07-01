package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// localLayer addresses a single image layer's COMPRESSED blob plus the metadata
// needed to decompress it. The compressed bytes are NOT held in memory for the
// real deploy path: TarPath/Offset/Size point at the layer's bytes inside the
// on-disk OCI tar, streamed on demand (see compressedReader). Decompression is
// deferred so callers that resolve a layer from the manifest cache never pay to
// read or decompress it. Blob is an in-memory fallback used by tests.
type localLayer struct {
	Digest    string // compressed OCI layer blob digest ("sha256:<hex>") — stable cache key
	DiffID    string // uncompressed layer digest from the image config's rootfs.diff_ids; "" when unavailable
	MediaType string // OCI/Docker layer media type (drives decompression)

	Blob []byte // compressed bytes, when held in memory (tests / small blobs)

	TarPath string // path to the OCI tar holding the compressed blob, when file-backed
	Offset  int64  // byte offset of the compressed blob within TarPath
	Size    int64  // compressed blob length
}

// compressedReader opens the layer's compressed bytes as a stream. The caller
// must Close it. File-backed layers reopen the OCI tar (one fd per call, so
// concurrent layers don't share state); in-memory layers wrap Blob.
func (l localLayer) compressedReader() (io.ReadCloser, error) {
	if l.TarPath != "" {
		f, err := os.Open(l.TarPath)
		if err != nil {
			return nil, fmt.Errorf("open OCI tar: %w", err)
		}
		return &sectionReadCloser{Reader: io.NewSectionReader(f, l.Offset, l.Size), c: f}, nil
	}
	return io.NopCloser(bytes.NewReader(l.Blob)), nil
}

// sectionReadCloser couples a SectionReader with the file it reads from so the
// fd is released on Close.
type sectionReadCloser struct {
	io.Reader
	c io.Closer
}

func (s *sectionReadCloser) Close() error { return s.c.Close() }

// decompress returns the raw (uncompressed) tar bytes for the layer in memory.
// Prefer decompressLayerToTemp for large layers so the tar never sits in RAM.
func (l localLayer) decompress() ([]byte, error) {
	cr, err := l.compressedReader()
	if err != nil {
		return nil, err
	}
	defer cr.Close()
	r, cleanup, err := layerTarReader(cr, l.MediaType)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decompress layer: %w", err)
	}
	return out, nil
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
func resolveOCIImageManifest(descs []ociDescriptor, getBlob func(hex string) ([]byte, error), wantOS, wantArch string, depth int) ([]byte, error) {
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
	blob, err := getBlob(hex)
	if err != nil {
		return nil, fmt.Errorf("manifest blob %s: %w", chosen.Digest, err)
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
		return resolveOCIImageManifest(nested.Manifests, getBlob, wantOS, wantArch, depth+1)
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

	// First pass: index each blob's byte range within the tar WITHOUT reading the
	// (potentially multi-GiB) layer bytes into memory. Only index.json is held;
	// manifest/config blobs are read back on demand below, and layer blobs are
	// streamed from the tar later via localLayer.compressedReader.
	blobOffsets := map[string]blobLoc{} // hex digest → byte range in the tar
	var indexJSON []byte

	cr := &offsetCountingReader{r: f}
	tr := tar.NewReader(cr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading OCI tar: %w", err)
		}
		// After Next() the counting reader sits exactly at this entry's data
		// (tar headers/padding are 512-byte blocks read straight from f).
		dataOff := cr.n
		switch {
		case hdr.Name == "index.json":
			indexJSON, err = io.ReadAll(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("reading index.json: %w", err)
			}
		case strings.HasPrefix(hdr.Name, "blobs/sha256/"):
			blobHex := strings.TrimPrefix(hdr.Name, "blobs/sha256/")
			blobOffsets[blobHex] = blobLoc{off: dataOff, size: hdr.Size}
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, nil, fmt.Errorf("scanning blob %q: %w", hdr.Name, err)
			}
		}
	}

	if indexJSON == nil {
		return nil, nil, fmt.Errorf("OCI tar missing index.json")
	}

	// getBlob reads a (small) blob — manifest, image-index, or config — back from
	// the tar by its recorded byte range. Layer blobs are NOT fetched this way;
	// they stay on disk and are streamed during the push.
	getBlob := func(hex string) ([]byte, error) {
		loc, ok := blobOffsets[hex]
		if !ok {
			return nil, fmt.Errorf("blob sha256:%s not found in OCI tar", hex)
		}
		return io.ReadAll(io.NewSectionReader(f, loc.off, loc.size))
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
	manifestData, err := resolveOCIImageManifest(index.Manifests, getBlob, wantOS, wantArch, 0)
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
	// WorkingDir/User) survives reassembly on the device. It also carries
	// rootfs.diff_ids — the uncompressed digest of each layer, in layer order —
	// which lets the push pre-check layer presence on the device WITHOUT
	// decompressing anything.
	var imageConfig []byte
	var diffIDs []string
	if manifest.Config.Digest != "" {
		configHex, err := digestToHex(manifest.Config.Digest)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid config digest %q: %w", manifest.Config.Digest, err)
		}
		imageConfig, err = getBlob(configHex)
		if err != nil {
			return nil, nil, fmt.Errorf("config blob %s: %w", manifest.Config.Digest, err)
		}
		var cfg struct {
			RootFS struct {
				DiffIDs []string `json:"diff_ids"`
			} `json:"rootfs"`
		}
		// A malformed/absent rootfs just leaves diffIDs empty: the push falls back
		// to deriving each diff ID by decompressing, so this is a pure optimization.
		if err := json.Unmarshal(imageConfig, &cfg); err == nil {
			diffIDs = cfg.RootFS.DiffIDs
		}
	}

	// diff_ids align 1:1 with manifest layers (both bottom-to-top, empty layers
	// excluded) per the OCI image-config spec. Only trust them to label layers
	// when the counts match; otherwise leave DiffID empty and let the push derive
	// it the slow way rather than risk mislabelling a layer.
	diffIDsAligned := len(diffIDs) == len(manifest.Layers)

	layers := make([]localLayer, 0, len(manifest.Layers))
	for i, desc := range manifest.Layers {
		layerHex, err := digestToHex(desc.Digest)
		if err != nil {
			return nil, nil, fmt.Errorf("layer %d: invalid digest %q: %w", i, desc.Digest, err)
		}
		loc, ok := blobOffsets[layerHex]
		if !ok {
			return nil, nil, fmt.Errorf("layer %d blob %s not found in OCI tar", i, desc.Digest)
		}
		var diffID string
		if diffIDsAligned {
			diffID = diffIDs[i]
		}
		// Reference the compressed blob by its range in the on-disk tar; it is
		// streamed (never fully buffered) during the push, and decompression is
		// deferred so cache-resolved layers are never read at all.
		layers = append(layers, localLayer{
			Digest:    desc.Digest,
			DiffID:    diffID,
			MediaType: desc.MediaType,
			TarPath:   ociTarPath,
			Offset:    loc.off,
			Size:      loc.size,
		})
	}
	return layers, imageConfig, nil
}

// blobLoc is a blob's byte range within an OCI-layout tar.
type blobLoc struct {
	off  int64
	size int64
}

// offsetCountingReader tracks how many bytes have been read from the wrapped
// reader, so the tar scan can record each entry's absolute data offset.
type offsetCountingReader struct {
	r io.Reader
	n int64
}

func (c *offsetCountingReader) Read(p []byte) (int, error) {
	m, err := c.r.Read(p)
	c.n += int64(m)
	return m, err
}

// layerTarReader wraps a compressed layer stream with the decompressor selected
// by media type, returning the raw (uncompressed) tar reader plus a cleanup func
// that releases the decompressor. The reader should be fully consumed first.
func layerTarReader(compressed io.Reader, mediaType string) (io.Reader, func(), error) {
	switch {
	case mediaType == "application/vnd.oci.image.layer.v1.tar" ||
		mediaType == "application/vnd.docker.image.rootfs.diff.tar":
		// Uncompressed — the stream is already the raw tar.
		return compressed, func() {}, nil

	case strings.HasSuffix(mediaType, ".tar+gzip") ||
		strings.HasSuffix(mediaType, ".tar.gzip") ||
		mediaType == "application/vnd.docker.image.rootfs.diff.tar.gzip":
		gr, err := gzip.NewReader(compressed)
		if err != nil {
			return nil, nil, fmt.Errorf("gzip reader: %w", err)
		}
		return gr, func() { _ = gr.Close() }, nil

	case strings.HasSuffix(mediaType, ".tar+zstd") ||
		strings.HasSuffix(mediaType, ".tar.zstd"):
		dec, err := zstd.NewReader(compressed)
		if err != nil {
			return nil, nil, fmt.Errorf("zstd reader: %w", err)
		}
		return dec, func() { dec.Close() }, nil

	default:
		return nil, nil, fmt.Errorf("unsupported layer media type: %q", mediaType)
	}
}

// decompressedLayer is a layer's uncompressed tar spilled to a temp file so the
// whole layer never resides in RAM. Chunk it via ChunkReaderAt(f, size) and
// read missing chunk bytes with f.ReadAt; call Close to delete the temp file.
type decompressedLayer struct {
	f      *os.File
	size   int64
	diffID string // "sha256:<hex>" of the uncompressed tar
}

// Close closes and removes the backing temp file. It is safe to call once.
func (d *decompressedLayer) Close() {
	name := d.f.Name()
	_ = d.f.Close()
	_ = os.Remove(name)
}

// decompressLayerToTemp streams the layer's uncompressed tar into a temp file,
// computing its DiffID as it writes. Peak memory is the decompressor window
// (a few MiB) rather than the whole layer. The returned file is positioned for
// random access via ReadAt; the caller must Close it.
func decompressLayerToTemp(l localLayer) (*decompressedLayer, error) {
	cr, err := l.compressedReader()
	if err != nil {
		return nil, err
	}
	defer cr.Close()
	r, cleanup, err := layerTarReader(cr, l.MediaType)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	f, err := os.CreateTemp("", "wendy-layer-*")
	if err != nil {
		return nil, fmt.Errorf("create layer temp file: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("decompress layer to disk: %w", err)
	}
	return &decompressedLayer{
		f:      f,
		size:   n,
		diffID: "sha256:" + hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// digestToHex converts a "sha256:<hex>" digest string to the bare hex portion.
func digestToHex(digest string) (string, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) {
		return "", fmt.Errorf("only sha256 digests supported, got %q", digest)
	}
	return strings.TrimPrefix(digest, prefix), nil
}

// imageBuildFailedError marks a failure of the actual image build (the buildx
// or Apple Container *solve* of the Dockerfile) in the chunk-diff deploy path,
// as opposed to a builder-setup or OCI-export failure. The registry-push
// fallback rebuilds the same image from the same Dockerfile, so a solve failure
// there recurs identically — falling back only buries the actionable build
// error behind a confusing secondary failure (e.g. buildx /etc/hosts setup).
// Callers surface this error directly instead of falling back. See issue #1166.
type imageBuildFailedError struct{ err error }

func (e *imageBuildFailedError) Error() string { return e.err.Error() }
func (e *imageBuildFailedError) Unwrap() error { return e.err }

// isImageBuildFailure reports whether err (or anything it wraps) is an
// imageBuildFailedError, i.e. the Dockerfile build itself failed.
func isImageBuildFailure(err error) bool {
	var bErr *imageBuildFailedError
	return errors.As(err, &bErr)
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
		"--progress", "plain",
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
		return &imageBuildFailedError{fmt.Errorf("docker buildx build (OCI export) failed: %w", err)}
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

	// --progress plain so the shared build parser can read the output (see
	// buildImageWithAppleContainer for the format rationale).
	args := []string{"build", "--progress", "plain", "--platform", platform, "-t", imageRef}
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
		return &imageBuildFailedError{fmt.Errorf("container build (OCI layout) failed: %w", err)}
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
