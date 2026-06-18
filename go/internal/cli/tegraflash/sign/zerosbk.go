// Package sign implements tegrasign_v3 ODM-open / zerosbk signing helpers:
// AES-128-CMAC with the all-zero key, SHA-512 digests, and the signed-manifest
// XML writer. These are the cryptographic primitives that tegrasign_v3.py
// produces for non-secure / zerosbk boot mode (no real key provisioned).
//
// The AES-CMAC "signature" is cosmetic in ODM-open: the Boot ROM does not
// enforce it. The integrity hash that the Boot ROM actually checks is the
// SHA-512 written into the Boot Component Header (BCH) or Boot Configuration
// Table (BCT) by tegrahost_v2 / tegrabct_v2 after tegrasign runs.
package sign

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/hex"
)

// ZeroCMAC computes AES-128-CMAC (RFC 4493) over data using the all-zero
// 16-byte key, matching tegrasign_v3's zerosbk behaviour. The input length
// is truncated to a multiple of 16 bytes before processing, replicating the
// NumAesBlocks truncation in tegrasign_v3.py sign_files_internal().
func ZeroCMAC(data []byte) [16]byte {
	// Truncate to a multiple of 16 (AES block size), as tegrasign does.
	n := (len(data) / 16) * 16
	return aesCMAC128(make([]byte, 16), data[:n])
}

// aesCMAC128 computes AES-128-CMAC (RFC 4493) over data using the given 16-byte key.
// data must already be truncated to the desired length by the caller.
func aesCMAC128(key, data []byte) [16]byte {
	var result [16]byte

	block, err := aes.NewCipher(key)
	if err != nil {
		// aes.NewCipher with a 16-byte key never returns an error.
		panic(err)
	}

	// Generate subkeys K1 and K2 per RFC 4493 section 2.3.
	const rb = byte(0x87)
	var L [16]byte
	block.Encrypt(L[:], L[:]) // AES_K(0^128)
	k1 := shiftLeft1(L)
	if L[0]&0x80 != 0 {
		k1[15] ^= rb
	}
	k2 := shiftLeft1(k1)
	if k1[0]&0x80 != 0 {
		k2[15] ^= rb
	}

	// Determine block count. With the empty input, use one block (padded).
	blockSize := 16
	numBlocks := len(data) / blockSize
	if numBlocks == 0 {
		numBlocks = 1
	}

	var X [16]byte
	for i := 0; i < numBlocks; i++ {
		var Y [16]byte
		start := i * blockSize
		end := start + blockSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[start:end]

		if i == numBlocks-1 {
			// Last block: XOR with K1 if complete, K2 if short (needs padding).
			padded := make([]byte, blockSize)
			copy(padded, chunk)
			if len(chunk) < blockSize {
				padded[len(chunk)] = 0x80 // ISO/IEC 7816-4 padding
				xorBlocks1(Y[:], X[:], padded, k2[:])
			} else {
				xorBlocks1(Y[:], X[:], padded, k1[:])
			}
		} else {
			xorBytes1(Y[:], X[:], chunk)
		}
		cipher.NewCBCEncrypter(block, make([]byte, 16)).CryptBlocks(X[:], Y[:])
	}

	copy(result[:], X[:])
	return result
}


func shiftLeft1(b [16]byte) [16]byte {
	var out [16]byte
	for i := 0; i < 15; i++ {
		out[i] = (b[i] << 1) | (b[i+1] >> 7)
	}
	out[15] = b[15] << 1
	return out
}

func xorBytes1(dst, a, b []byte) {
	for i := range dst {
		dst[i] = a[i] ^ b[i]
	}
}

func xorBlocks1(dst, a, b, c []byte) {
	for i := range dst {
		dst[i] = a[i] ^ b[i] ^ c[i]
	}
}

// SHA512 returns the SHA-512 digest of data.
func SHA512(data []byte) [64]byte {
	return sha512.Sum512(data)
}

// SHA512Hex returns the lower-case hexadecimal SHA-512 digest of data.
func SHA512Hex(data []byte) string {
	d := SHA512(data)
	return hex.EncodeToString(d[:])
}
