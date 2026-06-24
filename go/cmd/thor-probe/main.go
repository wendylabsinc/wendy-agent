// Command thor-probe is a read-only RCM diagnostic for validating the Jetson
// USB recovery path on macOS. It enumerates a device in recovery mode, reads
// the bootROM UID, and reads string descriptor 3 (the chip BR_CID), dumping the
// raw bytes so the decode can be verified on real hardware. It writes nothing
// to the device.
package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/rcm"
)

func main() {
	fmt.Println("thor-probe: waiting for a Jetson in USB recovery mode (PID 0x7023 Orin / 0x7026 Thor)...")
	dev, err := rcm.WaitForDevice()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	defer dev.Close()

	fmt.Printf("device:   %s\n", dev.String())
	fmt.Printf("productID: 0x%04x  isT264=%v\n", uint16(dev.ProductID()), dev.IsT264())

	// Note: no bulk-IN ReadUID here — it's destructive on macOS for T264 (times out →
	// libusb aborts the endpoint). The chip ID comes from the EP0 control read below.

	// Raw control read of string descriptor index 3. Dump the full response so the
	// decode can be verified on hardware: the payload is the BR_CID hex string,
	// reversed (NOT an RCM "state").
	buf := make([]byte, 96)
	n, err := dev.ControlRead(buf)
	if err != nil {
		fmt.Printf("ControlRead (string desc 3): FAILED: %v\n", err)
		fmt.Println("\n>> macOS IOKit may be rejecting the EP0 control transfer. Capture verbatim.")
	} else {
		fmt.Printf("ControlRead: %d bytes\n", n)
		fmt.Printf("  bLength=0x%02x bDescriptorType=0x%02x\n", safeByte(buf, 0), safeByte(buf, 1))
		fmt.Printf("  raw:\n%s", indent(hex.Dump(buf[:n])))
	}

	cid, err := dev.ReadChipID()
	if err != nil {
		fmt.Printf("ChipID: decode error: %v\n", err)
	} else {
		fmt.Printf("ChipID (BR_CID): 0x%s\n", cid)
	}
}

func safeByte(b []byte, i int) byte {
	if i < len(b) {
		return b[i]
	}
	return 0
}

func indent(s string) string {
	out := ""
	for _, line := range splitLines(s) {
		if line != "" {
			out += "    " + line + "\n"
		}
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			lines = append(lines, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
