package commands

import (
	"fmt"
	"io"
	"os"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go/pkg"
	"github.com/klauspost/compress/zstd"
)

// seekableImage is a random-access view over the decompressed bytes of a
// seekable-zstd image. It is NOT safe for concurrent use: callers drive it from
// a single goroutine (the flash loop). Close releases the decoder and file.
type seekableImage struct {
	r    *seekable.Reader
	dec  *zstd.Decoder
	f    *os.File // nil when constructed from an in-memory ReadSeeker (tests)
	size int64
}

// openSeekableZstd opens a seekable-zstd file on disk.
func openSeekableZstd(path string) (*seekableImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening seekable image: %w", err)
	}
	si, err := newSeekableImage(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	si.f = f
	return si, nil
}

// openSeekableZstdFromReader builds a seekableImage over an in-memory source.
func openSeekableZstdFromReader(rs io.ReadSeeker) (*seekableImage, error) {
	return newSeekableImage(rs)
}

func newSeekableImage(rs io.ReadSeeker) (*seekableImage, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("zstd decoder: %w", err)
	}
	r, err := seekable.NewReader(rs, dec)
	if err != nil {
		dec.Close()
		return nil, fmt.Errorf("seekable reader: %w", err)
	}
	table, err := r.SeekTable()
	if err != nil {
		r.Close()
		dec.Close()
		return nil, fmt.Errorf("seek table: %w", err)
	}
	return &seekableImage{r: r, dec: dec, size: int64(table.Size())}, nil
}

func (s *seekableImage) Size() int64 { return s.size }

func (s *seekableImage) ReadAt(p []byte, off int64) (int, error) { return s.r.ReadAt(p, off) }

func (s *seekableImage) Seek(off int64, whence int) (int64, error) { return s.r.Seek(off, whence) }

func (s *seekableImage) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *seekableImage) Close() error {
	err := s.r.Close()
	s.dec.Close()
	if s.f != nil {
		if e := s.f.Close(); err == nil {
			err = e
		}
	}
	return err
}
