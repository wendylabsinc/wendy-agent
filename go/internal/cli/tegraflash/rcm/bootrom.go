//go:build darwin || linux

package rcm

import (
	"fmt"
	"time"
)

// bootROMResponseTimeout bounds the optional post-image read. The T264 bootROM
// does not ACK every image — on a live T264, bct_br/mb1/psc_bl1 return nothing,
// while bct_mb1 (last) returned a short mb1 version string. A timeout here is
// expected, not an error.
const bootROMResponseTimeout = 750 * time.Millisecond

// DownloadBootROMImages performs the bootROM-stage RCM download sequence for the
// T23x bootROM family (NVIDIA's mainT23x; covers T234/Orin and T264/Thor, though
// only Thor is routed here — Orin uses the single-applet LoadApplet path). Each
// image is a complete, pre-signed RCM blob and is sent VERBATIM over bulk OUT
// (chunked + ZLP by Device.Write).
//
// images must be in bootROM-required order. The full T264 sequence observed from
// tegrarcm_v2 / a live device is bct_br → mb1 → psc_bl1 → bct_mb1 (then mb2 + the
// rest are delivered in a later --pollbl/bct_mem/blob phase). The BCTs are
// generated at flash time and must precede the firmware binaries.
//
// The bootROM does not send a status word for every image, so we do NOT block on
// one: after each write we do a brief, tolerant read to consume any response the
// bootROM does send and then move on. A genuine rejection resets the USB device,
// which surfaces as a write error on the next image.
//
// There is NO RCM40/DL_MINILOADER envelope at this stage and no queryable device
// "state": tegrarcm_v2 issues --new_session and downloads immediately. Wrapping the
// blobs (as an earlier version did via BuildDLMiniloader) makes the bootROM reject
// the message and reset the USB device.
func DownloadBootROMImages(dev *Device, images [][]byte) error {
	// Consume any pending bulk IN (e.g. the ECID the bootROM emits on connect)
	// before the first bulk OUT, or the write can stall.
	drainBulkIn(dev)

	for i, img := range images {
		// Sent verbatim — the blob is already a signed RCM message. Device.Write
		// handles 16 KiB chunking and the trailing zero-length packet.
		if err := dev.Write(img); err != nil {
			return fmt.Errorf("sending bootROM image %d (%d bytes): %w", i, len(img), err)
		}
		// Consume any response, but do not require one. Read into a full max-packet
		// buffer (a sub-packet read can error on macOS IOKit). Timeouts are normal.
		resp := make([]byte, 512)
		_, _ = dev.ReadWithTimeout(resp, bootROMResponseTimeout) //nolint:errcheck
	}
	return nil
}

// drainBulkIn reads and discards any pending data from the bulk IN endpoint.
// Used to consume the ECID that T264's bootROM emits on connect.
func drainBulkIn(dev *Device) {
	buf := make([]byte, 512)
	_, _ = dev.ReadWithTimeout(buf, 500*time.Millisecond) //nolint:errcheck
}
