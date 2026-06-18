//go:build darwin || linux

package rcm

import "fmt"

// LoadImagesT23x performs the T23x multi-image RCM download sequence used by
// T264 (Thor) devices. It probes the bootROM state via USB control transfer,
// then sends each image as a separate RCM40 DL_MINILOADER bulk write.
//
// images must be provided in bootROM-required order (mb1, psc_bl1, applet).
// The caller extracts the image list from Bundle.RCMImages().
//
// Protocol derived from RE of tegrarcm_v2 mainT23x (Thor nightly 20260618).
func LoadImagesT23x(dev *Device, images [][]byte) error {
	state, err := dev.RCMState()
	if err != nil {
		return fmt.Errorf("probing T23x RCM state: %w", err)
	}
	if state != 0 {
		return fmt.Errorf("unexpected T23x RCM state %d (want 0): power-cycle the device and retry", state)
	}

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
