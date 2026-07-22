package flashengine

// Host-side image transforms bootburn applies before flashing certain QSPI
// partitions: the BCT is padded and replicated into three copies, and the
// bad-page table is padded and replicated to fill its partition. Both produce a
// new local file whose MD5 must be recomputed for device-side verification.

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
)

const (
	bctBlockSize   = 262144 // 256 KiB — BCT copy stride (L4T)
	bctCopies      = 3
	badPageCopyLen = 262144 // 256 KiB per bad-page copy
	// l4tPadByte is the fill byte bootburn uses when padding BCT/bad-page copies
	// on L4T (append_padding_to_file: 0xff for L4T). Getting this wrong (e.g. 0x00)
	// makes the written partition's MD5 mismatch device-side — verified on hardware.
	l4tPadByte = 0xff
)

// padBlock returns a block of size n whose first len(data) bytes are data and the
// rest is the L4T pad byte (0xff).
func padBlock(data []byte, n int) []byte {
	block := make([]byte, n)
	for i := range block {
		block[i] = l4tPadByte
	}
	copy(block, data)
	return block
}

// transform returns the local file and MD5 to flash for a partition, applying the
// BCT/bad-page replication where bootburn would. For all other partitions it
// returns the original file and its manifest MD5 unchanged.
func (e *engine) transform(p partition, localFile string) (string, string, error) {
	switch p.Name {
	case "bct":
		return e.transformBCT(p, localFile)
	case "bad-page":
		return e.transformBadPage(p, localFile)
	default:
		return localFile, p.MD5, nil
	}
}

// transformBCT pads the BCT to bctBlockSize and writes bctCopies back-to-back
// (default boot chain A). Mirrors bootburn's L4T BCT replication.
func (e *engine) transformBCT(p partition, localFile string) (string, string, error) {
	data, err := os.ReadFile(localFile)
	if err != nil {
		return "", "", err
	}
	if len(data) >= 16384 {
		return "", "", fmt.Errorf("unexpected BCT size %d (want < 16384)", len(data))
	}
	if int64(bctBlockSize*bctCopies) > p.Size {
		return "", "", fmt.Errorf("BCT partition %d smaller than %d", p.Size, bctBlockSize*bctCopies)
	}
	out := bytes.Repeat(padBlock(data, bctBlockSize), bctCopies)
	return e.writeTemp("bct-replicated", out)
}

// transformBadPage pads the bad-page table to badPageCopyLen and replicates it to
// fill the partition. Mirrors bootburn's bad-page replication.
func (e *engine) transformBadPage(p partition, localFile string) (string, string, error) {
	data, err := os.ReadFile(localFile)
	if err != nil {
		return "", "", err
	}
	if len(data) >= badPageCopyLen {
		return "", "", fmt.Errorf("unexpected bad-page size %d (want < %d)", len(data), badPageCopyLen)
	}
	// Round up so the replicated copies fill the whole partition (bootburn appends
	// copies until it reaches p.Size). For an aligned p.Size this equals p.Size/len;
	// a non-multiple would overshoot and is rejected by flashPartition's
	// fileSize > p.Size guard, matching bootburn.
	copies := int((p.Size + badPageCopyLen - 1) / badPageCopyLen)
	if copies < 1 {
		copies = 1
	}
	out := bytes.Repeat(padBlock(data, badPageCopyLen), copies)
	return e.writeTemp("bad-page-replicated", out)
}

// writeTemp writes transformed bytes to a temp file and returns its path + MD5.
func (e *engine) writeTemp(tag string, data []byte) (string, string, error) {
	sum := md5.Sum(data)
	if e.opts.DryRun {
		e.logf("transform %s: %d bytes, md5=%s", tag, len(data), hex.EncodeToString(sum[:]))
	}
	// The transformed image is always written to a temp file: the caller Stats and
	// streams it, in dry-run too.
	f, err := os.CreateTemp("", "wendy-"+tag+"-*")
	if err != nil {
		return "", "", err
	}
	e.tempFiles = append(e.tempFiles, f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", "", err
	}
	if err := f.Close(); err != nil {
		return "", "", err
	}
	return f.Name(), hex.EncodeToString(sum[:]), nil
}
