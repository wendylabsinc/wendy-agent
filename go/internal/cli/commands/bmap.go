package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// BmapRange is one contiguous run of mapped blocks in a bmap file.
// First and Last are inclusive block indices.
type BmapRange struct {
	First    int64
	Last     int64
	Checksum string // hex-encoded SHA256 of the range's bytes
}

// Bmap is the parsed subset of a bmaptool v2.0 block-map file that we need to
// reconstruct an image: the block size, total image size, and the mapped
// ranges with their per-range checksums.
type Bmap struct {
	BlockSize int64
	ImageSize int64
	Ranges    []BmapRange
}

// xmlBmap mirrors the on-disk bmaptool XML format for unmarshalling.
type xmlBmap struct {
	ImageSize    int64  `xml:"ImageSize"`
	BlockSize    int64  `xml:"BlockSize"`
	ChecksumType string `xml:"ChecksumType"`
	Ranges       []struct {
		Checksum string `xml:"chksum,attr"`
		Value    string `xml:",chardata"`
	} `xml:"BlockMap>Range"`
}

// parseBmap decodes a bmaptool v2.0 block-map file. It rejects checksum types
// other than sha256 (the only type the build emits) so callers fall back to dd
// rather than skipping verification.
func parseBmap(data []byte) (*Bmap, error) {
	var x xmlBmap
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parsing bmap: %w", err)
	}
	if x.BlockSize <= 0 || x.ImageSize <= 0 {
		return nil, fmt.Errorf("bmap: invalid block size %d or image size %d", x.BlockSize, x.ImageSize)
	}
	if t := strings.ToLower(strings.TrimSpace(x.ChecksumType)); t != "sha256" {
		return nil, fmt.Errorf("bmap: unsupported checksum type %q", x.ChecksumType)
	}

	b := &Bmap{BlockSize: x.BlockSize, ImageSize: x.ImageSize}
	for _, r := range x.Ranges {
		first, last, err := parseRange(strings.TrimSpace(r.Value))
		if err != nil {
			return nil, err
		}
		b.Ranges = append(b.Ranges, BmapRange{
			First:    first,
			Last:     last,
			Checksum: strings.TrimSpace(r.Checksum),
		})
	}
	return b, nil
}

// bmapChunkSize is the read granularity within a mapped range. Package-level
// (not const) so tests can shrink it to exercise the multi-chunk path.
var bmapChunkSize int64 = 1 << 20

// applyBmap reconstructs an image onto dst using the block map. It reads src
// (the decompressed image) strictly sequentially — src may be a pipe — and
// writes only mapped ranges via WriteAt, discarding bytes that fall in holes.
// Each mapped range's SHA256 is verified against the bmap; a mismatch aborts.
// progressFn is called with the cumulative number of uncompressed bytes
// consumed from src.
func applyBmap(src io.Reader, dst io.WriterAt, b *Bmap, progressFn func(int64)) error {
	buf := make([]byte, bmapChunkSize)
	var consumed int64

	pos := int64(0) // next uncompressed byte offset we will read
	for _, r := range b.Ranges {
		start := r.First * b.BlockSize
		end := (r.Last + 1) * b.BlockSize
		if end > b.ImageSize {
			end = b.ImageSize
		}

		// Skip the hole before this range by reading and discarding.
		if start > pos {
			if err := discard(src, start-pos, buf, &consumed, progressFn); err != nil {
				return err
			}
			pos = start
		}

		// Stream the range to dst while hashing it.
		h := sha256.New()
		off := start
		for off < end {
			n := int64(len(buf))
			if rem := end - off; rem < n {
				n = rem
			}
			if _, err := io.ReadFull(src, buf[:n]); err != nil {
				return fmt.Errorf("reading mapped range at %d: %w", off, err)
			}
			if _, err := dst.WriteAt(buf[:n], off); err != nil {
				return fmt.Errorf("writing at %d: %w", off, err)
			}
			h.Write(buf[:n])
			off += n
			consumed += n
			progressFn(consumed)
		}
		pos = end

		if r.Checksum != "" {
			got := hex.EncodeToString(h.Sum(nil))
			if !strings.EqualFold(got, r.Checksum) {
				return fmt.Errorf("bmap: checksum mismatch for blocks %d-%d (got %s, want %s)",
					r.First, r.Last, got, r.Checksum)
			}
		}
	}

	// Drain any trailing holes so src is fully consumed (keeps the upstream
	// decompressor/pipe happy and makes progress reach 100%).
	if b.ImageSize > pos {
		if err := discard(src, b.ImageSize-pos, buf, &consumed, progressFn); err != nil {
			return err
		}
	}
	return nil
}

// discard reads exactly n bytes from src and throws them away, updating the
// running consumed counter and progress.
func discard(src io.Reader, n int64, buf []byte, consumed *int64, progressFn func(int64)) error {
	for n > 0 {
		m := int64(len(buf))
		if n < m {
			m = n
		}
		read, err := io.ReadFull(src, buf[:m])
		if read > 0 {
			*consumed += int64(read)
			progressFn(*consumed)
			n -= int64(read)
		}
		if err != nil {
			return fmt.Errorf("reading hole: %w", err)
		}
	}
	return nil
}

// mappedBytes returns the total number of mapped (non-hole) bytes the block map
// describes — the exact amount applyBmapSeekable will write. Used to size the
// progress bar for the seekable path.
func mappedBytes(b *Bmap) int64 {
	var total int64
	for _, r := range b.Ranges {
		start := r.First * b.BlockSize
		end := (r.Last + 1) * b.BlockSize
		if end > b.ImageSize {
			end = b.ImageSize
		}
		if end > start {
			total += end - start
		}
	}
	return total
}

// applyBmapSeekable reconstructs an image onto dst using the block map, reading
// only mapped ranges from a random-access source. It Seeks to each range's
// start, so the underlying seekable decoder never decodes hole frames — this is
// where the zero-block speedup comes from. Each range's SHA256 is verified
// against the bmap; a mismatch aborts. progressFn reports cumulative mapped
// bytes written.
func applyBmapSeekable(src io.ReadSeeker, dst io.WriterAt, b *Bmap, progressFn func(int64)) error {
	buf := make([]byte, bmapChunkSize)
	var written int64
	for _, r := range b.Ranges {
		start := r.First * b.BlockSize
		end := (r.Last + 1) * b.BlockSize
		if end > b.ImageSize {
			end = b.ImageSize
		}
		if end <= start {
			continue
		}
		if _, err := src.Seek(start, io.SeekStart); err != nil {
			return fmt.Errorf("seeking to %d: %w", start, err)
		}
		h := sha256.New()
		off := start
		for off < end {
			n := int64(len(buf))
			if rem := end - off; rem < n {
				n = rem
			}
			if _, err := io.ReadFull(src, buf[:n]); err != nil {
				return fmt.Errorf("reading mapped range at %d: %w", off, err)
			}
			if _, err := dst.WriteAt(buf[:n], off); err != nil {
				return fmt.Errorf("writing at %d: %w", off, err)
			}
			h.Write(buf[:n])
			off += n
			written += n
			progressFn(written)
		}
		if r.Checksum != "" {
			got := hex.EncodeToString(h.Sum(nil))
			if !strings.EqualFold(got, r.Checksum) {
				return fmt.Errorf("bmap: checksum mismatch for blocks %d-%d (got %s, want %s)",
					r.First, r.Last, got, r.Checksum)
			}
		}
	}
	return nil
}

// parseRange parses a bmap range token: either "N" or "N-M".
func parseRange(s string) (first, last int64, err error) {
	if s == "" {
		return 0, 0, fmt.Errorf("bmap: empty range")
	}
	dash := strings.IndexByte(s, '-')
	if dash < 0 {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("bmap: bad block number %q: %w", s, err)
		}
		return n, n, nil
	}
	first, err = strconv.ParseInt(s[:dash], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bmap: bad range start %q: %w", s, err)
	}
	last, err = strconv.ParseInt(s[dash+1:], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bmap: bad range end %q: %w", s, err)
	}
	if last < first {
		return 0, 0, fmt.Errorf("bmap: range end %d before start %d", last, first)
	}
	return first, last, nil
}
