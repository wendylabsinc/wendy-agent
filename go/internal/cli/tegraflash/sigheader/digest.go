package sigheader

import "crypto/sha512"

// Digest field offsets within the BCH.
const (
	offHeaderDigest      = 0x0004 // 64 bytes: SHA-512 of buf[0x44:0x2000]
	offSignedSectionHash = 0x0050 // 64 bytes: SHA-512 of buf[0xFC0:0x2000]
	offImageHash         = 0x1F30 // 64 bytes: SHA-512 of payload body (buf[0x2000:len-64])

	// offHeaderDigestCoverageStart is the first byte included in the header digest.
	offHeaderDigestCoverageStart = 0x0044

	// offSignedSectionStart is the first byte of the signed section.
	offSignedSectionStart = 0x0FC0

	// compSHA512Offset is the offset within a component table entry where the
	// 64-byte per-component SHA-512 digest is stored (relative to offCompTable).
	compSHA512FieldOff = offCompTable + compOffSHA512 // 0x1460
)

// recomputeDigests fills the three SHA-512 digest fields in buf.
//
// buf must be at least bchSize bytes long (i.e. bchSize + payload length).
//
// The three digests are computed in a specific order because the coverage
// ranges overlap: the signed-section digest field (0x50) lies inside the
// header-digest coverage range, and the image-hash field (0x1F30) lies inside
// the signed-section coverage range.
//
//  1. Image digest  @ 0x1F30 = SHA-512 of buf[0x2000 : len(buf)-64]
//     (the payload body, excluding the last 64 bytes which are the alignment
//     pad region reserved for a trailing signature placeholder)
//
//  2. Per-component digest @ 0x1460 = SHA-512 of buf[0x2000 : len(buf)]
//     (the full aligned payload)
//
//  3. Signed-section digest @ 0x50 = SHA-512 of buf[0xFC0:0x2000]
//     (written after the image digest so 0x1F30 is already set)
//
//  4. Header digest @ 0x04 = SHA-512 of buf[0x44:0x2000]
//     (written last so both 0x50 and 0x1F30 are already set)
func recomputeDigests(buf []byte) {
	// 1. Image digest: payload body = buf[0x2000 : len(buf)-64].
	//    tegrahost_v2 uses the pre-alignment (original) image length which is
	//    len(payload) - 64 for a payload that was padded to the next 512-byte
	//    boundary from a 64-byte-smaller original.
	imgHash := sha512.Sum512(buf[bchSize : len(buf)-64])
	copy(buf[offImageHash:], imgHash[:])

	// 2. Per-component SHA-512 of the full aligned payload.
	compHash := sha512.Sum512(buf[bchSize:])
	copy(buf[compSHA512FieldOff:], compHash[:])

	// 3. Signed-section digest.
	secHash := sha512.Sum512(buf[offSignedSectionStart:bchSize])
	copy(buf[offSignedSectionHash:], secHash[:])

	// 4. Header digest (must be last).
	hdrHash := sha512.Sum512(buf[offHeaderDigestCoverageStart:bchSize])
	copy(buf[offHeaderDigest:], hdrHash[:])
}
