//go:build darwin || linux

package rcm

import (
	"context"
	"fmt"
	"time"
)

// DownloadBootROMImages performs the bootROM-stage RCM download sequence for the
// T23x bootROM family (NVIDIA's mainT23x; covers T234/Orin and T264/Thor, though
// only Thor is routed here — Orin uses the single-applet LoadApplet path). Each
// image is a complete, pre-signed "NVDA" RCM blob and is sent VERBATIM over bulk
// OUT (chunked + ZLP by Device.Write); after each, a 4-byte status word is read.
//
// images must be in bootROM-required order. The full T264 sequence observed from
// tegrarcm_v2 is bct_br → mb1 → psc_bl1 → bct_mb1 (then the mb2 applet is delivered
// separately over nv3p). The BCTs are generated at flash time and must precede the
// firmware binaries.
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
		status := make([]byte, 4)
		if _, err := dev.Read(status); err != nil {
			return fmt.Errorf("reading status after bootROM image %d: %w", i, err)
		}
	}
	return nil
}

// drainBulkIn reads and discards any pending data from the bulk IN endpoint.
// Used to consume the ECID that T264's bootROM emits on connect.
func drainBulkIn(dev *Device) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	buf := make([]byte, 512)
	dev.in.ReadContext(ctx, buf) //nolint:errcheck
}
