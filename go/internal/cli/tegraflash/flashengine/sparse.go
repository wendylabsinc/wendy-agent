package flashengine

// Android ext4 sparse image (.simg) streaming, mirroring bootburn's
// SendSparseFile: parse the sparse header and per-chunk headers, then for each
// RAW chunk stream its data to the partition (in maxChunk pieces so device /tmp
// stays bounded), for each non-zero FILL chunk write the repeated fill value, and
// for DONTCARE / zero-FILL / CRC chunks just advance the write offset. This lets
// a multi-GB rootfs flash without materializing it on the device.

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

const (
	sparseMagic       = 0xed26ff3a
	chunkTypeRaw      = 0xCAC1
	chunkTypeFill     = 0xCAC2
	chunkTypeDontCare = 0xCAC3
	chunkTypeCRC      = 0xCAC4
)

type sparseHeader struct {
	Magic         uint32
	Major, Minor  uint16
	FileHdrSize   uint16
	ChunkHdrSize  uint16
	BlockSize     uint32
	TotalBlocks   uint32
	TotalChunks   uint32
	ImageChecksum uint32
}

// writeSparse streams a sparse image to p.Device starting at p.Start.
func (e *engine) writeSparse(p partition, localFile string) error {
	f, err := os.Open(localFile)
	if err != nil {
		return err
	}
	defer f.Close()

	var h sparseHeader
	if err := binary.Read(f, binary.LittleEndian, &h); err != nil {
		return fmt.Errorf("reading sparse header: %w", err)
	}
	if h.Magic != sparseMagic {
		return fmt.Errorf("not a sparse image (magic 0x%08x)", h.Magic)
	}
	// The whole-file/chunked paths guard against an image larger than the partition
	// before dispatch, but there fileSize is the compressed sparse size; guard the
	// expanded size here so a wrong manifest Size can't stream writes past the
	// partition into the next one.
	if expanded := int64(h.TotalBlocks) * int64(h.BlockSize); expanded > p.Size {
		return fmt.Errorf("sparse image expands to %d bytes, larger than partition %s (%d)", expanded, p.Name, p.Size)
	}
	// Skip any extra file-header bytes beyond the 28 we read.
	if int(h.FileHdrSize) > 28 {
		if _, err := f.Seek(int64(h.FileHdrSize), io.SeekStart); err != nil {
			return err
		}
	}
	fmt.Fprintf(e.out, "  sparse: %d chunks, block=%d, blocks=%d\n", h.TotalChunks, h.BlockSize, h.TotalBlocks)

	// Discard the target region first (bootburn SendSparseFile): skipped DONTCARE
	// and zero-FILL chunks are only correct if the underlying blocks read back as
	// zeros. Failure is tolerated like bootburn (slower, but writes still land).
	e.shellTolerant(fmt.Sprintf("blkdiscard -f %s -o %d -l %d", p.Device, p.Start, p.Size))

	base := "simg"
	var written int64 // bytes advanced within the partition
	for i := uint32(0); i < h.TotalChunks; i++ {
		var ch struct {
			Type     uint16
			Reserved uint16
			ChunkSz  uint32 // in blocks
			TotalSz  uint32 // chunk incl header
		}
		if err := binary.Read(f, binary.LittleEndian, &ch); err != nil {
			return fmt.Errorf("reading chunk %d header: %w", i, err)
		}
		// Skip any extra chunk-header bytes.
		if int(h.ChunkHdrSize) > 12 {
			if _, err := f.Seek(int64(h.ChunkHdrSize)-12, io.SeekCurrent); err != nil {
				return err
			}
		}
		regionLen := int64(ch.ChunkSz) * int64(h.BlockSize)

		switch ch.Type {
		case chunkTypeRaw:
			if err := e.streamRegion(p, f, base, written, regionLen); err != nil {
				return fmt.Errorf("raw chunk %d: %w", i, err)
			}
		case chunkTypeFill:
			var fill [4]byte
			if _, err := io.ReadFull(f, fill[:]); err != nil {
				return err
			}
			if fill != [4]byte{} {
				if err := e.streamFill(p, base, written, regionLen, fill); err != nil {
					return fmt.Errorf("fill chunk %d: %w", i, err)
				}
			}
			// zero fill: skip (leave device as-is), matching bootburn.
		case chunkTypeDontCare:
			// nothing to write; advance offset.
		case chunkTypeCRC:
			if _, err := f.Seek(4, io.SeekCurrent); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown sparse chunk type 0x%04x at chunk %d", ch.Type, i)
		}
		written += regionLen
	}
	return nil
}

// streamRegion writes regionLen bytes read from r to the partition at offset
// written, in maxChunk pieces (push + nvdd + rm each).
func (e *engine) streamRegion(p partition, r io.Reader, base string, written, regionLen int64) error {
	buf := make([]byte, maxChunk)
	var done int64
	sub := 0
	for done < regionLen {
		n := int64(len(buf))
		if regionLen-done < n {
			n = regionLen - done
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return err
		}
		remote := fmt.Sprintf("%s/%s_%d_%d", remoteTmp, base, written, sub)
		if err := e.pushBytes(buf[:n], remote, 0o644); err != nil {
			return err
		}
		cmd := fmt.Sprintf("%s --inputbin=%s --partsize %d --device %s --startoffset=%d --l4t",
			remoteNvdd, remote, n, p.Device, p.Start+written+done)
		if err := e.shell(cmd); err != nil {
			return err
		}
		if err := e.shell("rm " + remote); err != nil {
			return err
		}
		done += n
		sub++
	}
	return nil
}

// streamFill writes a repeated 4-byte fill value across regionLen bytes.
func (e *engine) streamFill(p partition, base string, written, regionLen int64, fill [4]byte) error {
	// Build one maxChunk buffer of the repeated fill, reused per sub-chunk.
	block := make([]byte, maxChunk)
	for i := 0; i < len(block); i += 4 {
		copy(block[i:], fill[:])
	}
	var done int64
	sub := 0
	for done < regionLen {
		n := int64(maxChunk)
		if regionLen-done < n {
			n = regionLen - done
		}
		remote := fmt.Sprintf("%s/%s_fill_%d_%d", remoteTmp, base, written, sub)
		if err := e.pushBytes(block[:n], remote, 0o644); err != nil {
			return err
		}
		cmd := fmt.Sprintf("%s --inputbin=%s --partsize %d --device %s --startoffset=%d --l4t",
			remoteNvdd, remote, n, p.Device, p.Start+written+done)
		if err := e.shell(cmd); err != nil {
			return err
		}
		if err := e.shell("rm " + remote); err != nil {
			return err
		}
		done += n
		sub++
	}
	return nil
}
