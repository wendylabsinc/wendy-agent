//go:build darwin || linux

package rcm

import "fmt"

// LoadImagesT23x performs the T23x multi-image RCM download sequence used by
// T264 (Thor) devices. Each image is a pre-built, signed RCM message blob (it
// begins with the "NVDA" magic) and is written to the bulk OUT endpoint VERBATIM
// — the bootROM rejects (and resets the device) if the blob is re-wrapped in
// another RCM header. After each blob a 4-byte status word is read back.
//
// images must be provided in bootROM-required order: bct_br, mb1, psc_bl1,
// bct_mb1. The bct_br and bct_mb1 blobs are generated from the bundle's BCT
// config at flash time (see tegraflash_impl_t264.tegraflash_generate_bct); a
// bundle that lacks them cannot complete the bootROM phase.
//
// Protocol derived from RE of tegrarcm_v2 RcmDownLoadImages (Thor nightly 20260618).
func LoadImagesT23x(dev *Device, images [][]byte) error {
	for i, blob := range images {
		if err := dev.Write(blob); err != nil {
			return fmt.Errorf("sending RCM blob %d: %w", i, err)
		}
		status := make([]byte, 4)
		if _, err := dev.Read(status); err != nil {
			// The final blob may reset the device before a status word is sent;
			// treat a read error only on the last blob as success.
			if i < len(images)-1 {
				return fmt.Errorf("reading status after RCM blob %d: %w", i, err)
			}
		}
	}
	return nil
}

