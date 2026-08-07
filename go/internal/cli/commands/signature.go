package commands

import (
	"errors"
	"os"
)

// readOptionalSignature reads a detached artifact signature (e.g. an ML-DSA65
// signature over the SHA256 digest of an agent binary, or of an OCI image config) from an
// optional sidecar file. No signer exists yet, so this is forward-compatible
// plumbing: callers pass an empty path today and get (nil, nil), which lands
// as an empty proto bytes field. The agent-side verifier already tolerates an
// absent signature (enforcement is gated on a pinned key being embedded,
// which is not the case yet) — see internal/shared/sigverify.
//
// A missing file at a non-empty path is NOT an error (the caller may not have
// produced one yet); any other read failure (permissions, a directory, etc.)
// is returned so misconfiguration is visible immediately, before any bytes
// are streamed to the device.
func readOptionalSignature(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}
