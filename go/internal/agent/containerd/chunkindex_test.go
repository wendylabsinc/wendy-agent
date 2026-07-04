package containerd

import (
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
)

func TestChunkIndexAddHasMissing(t *testing.T) {
	ix, err := NewChunkIndex(filepath.Join(t.TempDir(), "idx.json"))
	if err != nil {
		t.Fatal(err)
	}
	h1 := [32]byte{1}
	h2 := [32]byte{2}
	ix.AddLayer("sha256:aaa", []chunk.Ref{{Hash: h1, Offset: 0, Len: 10}})

	if _, ok := ix.Has(h1); !ok {
		t.Fatal("expected h1 present")
	}
	missing := ix.Missing([][32]byte{h1, h2})
	if len(missing) != 1 || missing[0] != h2 {
		t.Fatalf("expected only h2 missing, got %v", missing)
	}
}

func TestChunkIndexPersistsAndDrops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.json")
	ix, _ := NewChunkIndex(path)
	h := [32]byte{9}
	ix.AddLayer("sha256:bbb", []chunk.Ref{{Hash: h, Offset: 5, Len: 7}})
	if err := ix.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewChunkIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Has(h); !ok {
		t.Fatal("expected h present after reload")
	}
	reloaded.Drop("sha256:bbb")
	if _, ok := reloaded.Has(h); ok {
		t.Fatal("expected h dropped")
	}
}
