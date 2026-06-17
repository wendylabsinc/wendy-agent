package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// cachedManifest is the persisted chunk manifest for one OCI layer, keyed by
// the layer's compressed blob digest. Because the compressed digest is stable
// across builds for an unchanged layer (buildx reuses cached layers), a hit
// lets the CLI skip decompressing and re-chunking that layer entirely.
type cachedManifest struct {
	DiffID string   `json:"diff_id"` // "sha256:<hex>" of the uncompressed tar
	Size   int64    `json:"size"`    // length of the uncompressed tar
	Hashes [][]byte `json:"hashes"`  // ordered raw 32-byte chunk sha256 digests
}

// manifestCacheTestDir, when non-empty, overrides the cache directory. Tests
// set it to a temp dir so they neither read nor pollute the real user cache.
var manifestCacheTestDir string

// manifestCacheDir returns the on-disk directory for cached layer manifests,
// creating it if needed. Returns ("", false) when no cache location is
// available (caching is then silently skipped).
func manifestCacheDir() (string, bool) {
	if manifestCacheTestDir != "" {
		return manifestCacheTestDir, true
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", false
	}
	dir := filepath.Join(base, "wendy", "chunkmanifest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false
	}
	return dir, true
}

// manifestCachePath maps a compressed layer digest ("sha256:<hex>") to its
// cache file path. The digest is sanitised to a flat filename.
func manifestCachePath(dir, layerDigest string) string {
	name := strings.NewReplacer(":", "_", "/", "_").Replace(layerDigest) + ".json"
	return filepath.Join(dir, name)
}

// loadManifestCache returns the cached manifest for a compressed layer digest,
// or (nil, false) on any miss/error (treated as a cache miss).
func loadManifestCache(layerDigest string) (*cachedManifest, bool) {
	dir, ok := manifestCacheDir()
	if !ok || layerDigest == "" {
		return nil, false
	}
	data, err := os.ReadFile(manifestCachePath(dir, layerDigest))
	if err != nil {
		return nil, false
	}
	var m cachedManifest
	if err := json.Unmarshal(data, &m); err != nil || m.DiffID == "" {
		return nil, false
	}
	return &m, true
}

// saveManifestCache persists a manifest for a compressed layer digest. Failures
// are non-fatal (the manifest is simply recomputed next time).
func saveManifestCache(layerDigest string, m *cachedManifest) {
	dir, ok := manifestCacheDir()
	if !ok || layerDigest == "" {
		return
	}
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	path := manifestCachePath(dir, layerDigest)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
