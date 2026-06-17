package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// sha256Hex returns the lowercase hex-encoded SHA-256 digest of b.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// writeOCITar writes a tar file at path containing the provided entries
// (name → data).
func writeOCITar(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	// Deterministic entry order: oci-layout, index.json, then blobs.
	orderedNames := []string{"oci-layout", "index.json"}
	blobNames := []string{}
	for name := range entries {
		if name != "oci-layout" && name != "index.json" {
			blobNames = append(blobNames, name)
		}
	}
	orderedNames = append(orderedNames, blobNames...)
	for _, name := range orderedNames {
		data, ok := entries[name]
		if !ok {
			continue
		}
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeMinimalOCILayout builds a minimal OCI-layout tar at path with a single
// layer whose blob bytes are blobData. mediaType is the layer media type.
// For uncompressed layers pass the raw tar as blobData; for compressed layers
// pass the compressed form. diffIDBytes is the UNCOMPRESSED bytes used to
// compute the DiffID in the config (for uncompressed layers it equals blobData).
func writeMinimalOCILayout(t *testing.T, path string, blobData []byte, mediaType string, diffIDBytes []byte) {
	t.Helper()

	diffID := "sha256:" + sha256Hex(diffIDBytes)
	layerDigest := "sha256:" + sha256Hex(blobData)

	configBytes := []byte(`{"architecture":"amd64","os":"linux","config":{"Cmd":["python","app.py"],"WorkingDir":"/app"},"rootfs":{"type":"layers","diff_ids":["` + diffID + `"]}}`)
	configDigest := "sha256:" + sha256Hex(configBytes)

	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    configDigest,
			"size":      len(configBytes),
		},
		"layers": []map[string]any{
			{
				"mediaType": mediaType,
				"digest":    layerDigest,
				"size":      len(blobData),
			},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := "sha256:" + sha256Hex(manifestBytes)

	index := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{
			{
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"digest":    manifestDigest,
				"size":      len(manifestBytes),
			},
		},
	}
	indexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}

	entries := map[string][]byte{
		"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`),
		"index.json": indexBytes,
		"blobs/sha256/" + sha256Hex(manifestBytes): manifestBytes,
		"blobs/sha256/" + sha256Hex(configBytes):   configBytes,
		"blobs/sha256/" + sha256Hex(blobData):      blobData,
	}
	writeOCITar(t, path, entries)
}

func TestReadOCILayoutLayersUncompressed(t *testing.T) {
	dir := t.TempDir()
	ociTar := filepath.Join(dir, "image.tar")
	want := []byte("hello-tar-bytes")
	writeMinimalOCILayout(t, ociTar, want, "application/vnd.oci.image.layer.v1.tar", want)

	layers, imageConfig, err := readOCILayoutLayers(ociTar)
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(layers))
	}
	got, err := layers[0].decompress()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("layer bytes mismatch: got %q, want %q", got, want)
	}
	if layers[0].Digest != "sha256:"+sha256Hex(want) {
		t.Fatalf("layer digest mismatch: %s", layers[0].Digest)
	}
	// The image config blob must be returned and carry the runtime config
	// (Cmd/WorkingDir); otherwise the assembled container would have no command
	// and exit immediately.
	if len(imageConfig) == 0 {
		t.Fatal("expected non-empty image config blob")
	}
	if !bytes.Contains(imageConfig, []byte(`"app.py"`)) || !bytes.Contains(imageConfig, []byte(`/app`)) {
		t.Fatalf("image config missing Cmd/WorkingDir: %s", imageConfig)
	}
}

func TestReadOCILayoutLayersGzip(t *testing.T) {
	dir := t.TempDir()
	ociTar := filepath.Join(dir, "image.tar")
	want := []byte("hello-tar-bytes-gzip")

	// Gzip-compress the layer bytes to store in the OCI layout.
	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	if _, err := gw.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	compressedBytes := compressed.Bytes()

	writeMinimalOCILayout(t, ociTar, compressedBytes, "application/vnd.oci.image.layer.v1.tar+gzip", want)

	layers, _, err := readOCILayoutLayers(ociTar)
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(layers))
	}
	got, err := layers[0].decompress()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("layer bytes mismatch after gzip decompression")
	}
	// The layer digest is the COMPRESSED blob digest (the stable cache key).
	if layers[0].Digest != "sha256:"+sha256Hex(compressedBytes) {
		t.Fatalf("layer digest mismatch (should be sha256 of compressed blob): %s", layers[0].Digest)
	}
}
