//go:build darwin || linux

package rcm

import (
	"context"
	"fmt"
	"time"
)

// LoadImagesT23x performs the T23x multi-image RCM download sequence used by
// T264 (Thor) devices, sending each image as a separate RCM40 DL_MINILOADER
// bulk write.
//
// images must be provided in bootROM-required order. The full T264 bootROM
// sequence observed from tegrarcm_v2 is bct_br → mb1 → psc_bl1 → bct_mb1 →
// applet; the BCTs are generated at flash time and must precede the binaries.
//
// There is no queryable "RCM state" gate: tegrarcm_v2 issues --new_session and
// then downloads immediately. (String descriptor index 3 carries the chip
// BR_CID, not a state — see Device.ReadChipID.)
//
// Protocol derived from RE of tegrarcm_v2 mainT23x (Thor nightly 20260618).
func LoadImagesT23x(dev *Device, images [][]byte) error {
	// Consume any pending bulk IN (e.g. the ECID the bootROM emits on connect)
	// before the first bulk OUT, or the write can stall.
	drainBulkIn(dev)

	for i, img := range images {
		msg, err := BuildDLMiniloader(img, [48]byte{})
		if err != nil {
			return fmt.Errorf("building RCM40 message for image %d: %w", i, err)
		}
		if err := dev.Write(msg); err != nil {
			return fmt.Errorf("sending image %d via RCM40: %w", i, err)
		}
		status := make([]byte, 4)
		if _, err := dev.Read(status); err != nil {
			// The applet (final image) causes an immediate device reset; the
			// bootROM may not send a status word before the USB connection drops.
			// Treat a read error only on the last image as success.
			if i < len(images)-1 {
				return fmt.Errorf("reading status after image %d: %w", i, err)
			}
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
