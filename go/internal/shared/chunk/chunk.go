// Package chunk provides content-defined chunking shared by the CLI and agent
// for sub-layer diffing. Boundaries come from desync's buzhash chunker; each
// chunk is identified by the sha256 of its bytes.
package chunk

import (
	"crypto/sha256"
	"io"

	"github.com/folbricht/desync"
)

const (
	MinSize uint64 = 16 << 10
	AvgSize uint64 = 64 << 10
	MaxSize uint64 = 256 << 10
)

// Ref identifies one content-defined chunk and its position in the stream.
type Ref struct {
	Hash   [32]byte
	Offset uint64
	Len    uint64
}

// Chunk reads r to completion and returns its content-defined chunks in order.
func Chunk(r io.Reader) ([]Ref, error) {
	c, err := desync.NewChunker(r, MinSize, AvgSize, MaxSize)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for {
		start, buf, err := c.Next()
		if err != nil {
			return nil, err
		}
		if len(buf) == 0 {
			break // end of stream
		}
		refs = append(refs, Ref{
			Hash:   sha256.Sum256(buf),
			Offset: start,
			Len:    uint64(len(buf)),
		})
	}
	return refs, nil
}
