package containerd

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
)

type chunkLoc struct {
	Blob   string `json:"blob"`
	Offset uint64 `json:"offset"`
	Len    uint64 `json:"len"`
}

// ChunkIndex maps content-defined chunk hashes to byte ranges inside
// uncompressed Wendy layer blobs already in the containerd content store.
type ChunkIndex struct {
	path    string
	mu      sync.Mutex
	entries map[string]chunkLoc // key: hex(hash)
}

func NewChunkIndex(path string) (*ChunkIndex, error) {
	ix := &ChunkIndex{path: path, entries: make(map[string]chunkLoc)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ix, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &ix.entries); err != nil {
			return nil, err
		}
	}
	return ix, nil
}

func key(h [32]byte) string { return hex.EncodeToString(h[:]) }

func (ix *ChunkIndex) Has(h [32]byte) (chunkLoc, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	loc, ok := ix.entries[key(h)]
	return loc, ok
}

func (ix *ChunkIndex) Missing(hashes [][32]byte) [][32]byte {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var missing [][32]byte
	for _, h := range hashes {
		if _, ok := ix.entries[key(h)]; !ok {
			missing = append(missing, h)
		}
	}
	return missing
}

func (ix *ChunkIndex) AddLayer(blobDigest string, refs []chunk.Ref) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, r := range refs {
		ix.entries[key(r.Hash)] = chunkLoc{Blob: blobDigest, Offset: r.Offset, Len: r.Len}
	}
}

func (ix *ChunkIndex) Drop(blobDigest string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for k, loc := range ix.entries {
		if loc.Blob == blobDigest {
			delete(ix.entries, k)
		}
	}
}

func (ix *ChunkIndex) Save() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	data, err := json.Marshal(ix.entries)
	if err != nil {
		return err
	}
	tmp := ix.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(ix.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ix.path)
}
