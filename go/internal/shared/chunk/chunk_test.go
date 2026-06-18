package chunk

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestChunkIsDeterministic(t *testing.T) {
	data := make([]byte, 2<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	a, err := Chunk(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Chunk(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("non-deterministic chunk count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("chunk %d differs between runs", i)
		}
	}
}

func TestChunkCoversStreamContiguously(t *testing.T) {
	data := make([]byte, 1<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	refs, err := Chunk(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var off uint64
	for i, r := range refs {
		if r.Offset != off {
			t.Fatalf("chunk %d offset gap: got %d want %d", i, r.Offset, off)
		}
		off += r.Len
	}
	if off != uint64(len(data)) {
		t.Fatalf("chunks cover %d bytes, want %d", off, len(data))
	}
}
