package commands

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
)

// The temp-file decompression path must be byte-for-byte equivalent to the
// in-memory path: same DiffID, same size, and the chunks read back from the
// file must match chunking the decompressed bytes directly. Any divergence
// would corrupt device-side reassembly.
func TestDecompressLayerToTempMatchesInMemory(t *testing.T) {
	// A multi-region payload so the parallel/region seams are exercised too.
	raw := make([]byte, (16<<20)+98765)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}

	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	if _, err := gw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	l := localLayer{
		Digest:    "sha256:" + hex.EncodeToString(sha256.New().Sum(gz.Bytes())),
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		Blob:      gz.Bytes(),
	}

	dl, err := decompressLayerToTemp(l)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Close()

	wantSum := sha256.Sum256(raw)
	wantDiffID := "sha256:" + hex.EncodeToString(wantSum[:])
	if dl.diffID != wantDiffID {
		t.Fatalf("diffID mismatch: got %s want %s", dl.diffID, wantDiffID)
	}
	if dl.size != int64(len(raw)) {
		t.Fatalf("size mismatch: got %d want %d", dl.size, len(raw))
	}

	fileRefs, err := chunk.ChunkReaderAt(dl.f, dl.size)
	if err != nil {
		t.Fatal(err)
	}
	memRefs, err := chunk.ChunkBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(fileRefs) != len(memRefs) {
		t.Fatalf("chunk count mismatch: file %d vs mem %d", len(fileRefs), len(memRefs))
	}
	for i := range fileRefs {
		if fileRefs[i] != memRefs[i] {
			t.Fatalf("chunk %d differs between temp-file and in-memory chunking", i)
		}
		// The bytes read back from the temp file must hash to the chunk hash.
		buf := make([]byte, fileRefs[i].Len)
		if _, err := dl.f.ReadAt(buf, int64(fileRefs[i].Offset)); err != nil {
			t.Fatalf("chunk %d ReadAt: %v", i, err)
		}
		if sha256.Sum256(buf) != fileRefs[i].Hash {
			t.Fatalf("chunk %d bytes read from temp file do not match its hash", i)
		}
	}
}
