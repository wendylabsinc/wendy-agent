//go:build darwin || linux

package rcm

import "fmt"

// DownloadBootROMImages performs the bootROM-stage RCM download sequence for the
// T23x bootROM family (NVIDIA's mainT23x; covers T234/Orin and T264/Thor, though
// only Thor is routed here — Orin uses the single-applet LoadApplet path). Each
// image is a complete, pre-signed RCM blob and is sent VERBATIM over bulk OUT
// (chunked + ZLP by Device.Write).
//
// images must be in bootROM-required order. The full T264 sequence observed from
// tegrarcm_v2 / a live device is bct_br → mb1 → psc_bl1 → bct_mb1, after which mb1
// runs and the device re-enumerates for the --pollbl/bct_mem/blob phase. The BCTs
// are generated at flash time and must precede the firmware binaries.
//
// We deliberately do NOT read a status word between images. On a live T264 the
// bootROM ACKs none of bct_br/mb1/psc_bl1 (only bct_mb1 returned a version string),
// so a per-image read just times out — and on macOS a timed-out bulk-IN read is
// destructive (libusb aborts the endpoint + clears the pipe stall, libusb #1110).
// A genuine rejection resets the USB device, which surfaces as the next write (or
// the following phase) failing.
//
// There is NO RCM40/DL_MINILOADER envelope at this stage and no queryable device
// "state": tegrarcm_v2 issues --new_session and downloads immediately. Wrapping the
// blobs (as an earlier version did via BuildDLMiniloader) makes the bootROM reject
// the message and reset the USB device.
func DownloadBootROMImages(dev *Device, images [][]byte) error {
	for i, img := range images {
		// Sent verbatim — the blob is already a signed RCM message. Device.Write
		// handles 16 KiB chunking and the trailing zero-length packet.
		if err := dev.Write(img); err != nil {
			return fmt.Errorf("sending bootROM image %d (%d bytes): %w", i, len(img), err)
		}
	}
	return nil
}
