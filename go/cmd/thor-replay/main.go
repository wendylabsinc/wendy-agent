// Command thor-replay drives the T264 bootROM RCM download sequence using
// pre-generated artifact files supplied on the command line, in order. It is the
// "capture and replay" harness: generate the zerosign BCTs/blobs on a Linux host
// with real tegraflash, then replay them at a Thor from a Mac.
//
// Usage:
//
//	thor-replay <bct_br> <mb1> <psc_bl1> <bct_mb1> [...]
//
// Unlike thor-probe this WRITES to the device (sends each blob verbatim over RCM).
// It does not attempt the later nv3p/applet phases.
package main

import (
	"fmt"
	"os"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/rcm"
)

func main() {
	paths := os.Args[1:]
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: thor-replay <image1> <image2> ...  (bootROM order, e.g. bct_br mb1 psc_bl1 bct_mb1)")
		os.Exit(2)
	}

	var blobs [][]byte
	fmt.Println("Images to send (verbatim, in order):")
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL reading %s: %v\n", p, err)
			os.Exit(1)
		}
		fmt.Printf("  [%d] %-40s %8d bytes  magic=%s\n", i, p, len(data), magic(data))
		blobs = append(blobs, data)
	}

	fmt.Println("\nWaiting for a Jetson in USB recovery mode...")
	dev, err := rcm.WaitForDevice()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	defer dev.Close()
	fmt.Printf("device: %s  isT264=%v\n", dev.String(), dev.IsT264())
	if cid, err := dev.ReadChipID(); err == nil {
		fmt.Printf("chipID (BR_CID): 0x%s\n", cid)
	}

	fmt.Printf("\nSending %d images via bootROM RCM sequence...\n", len(blobs))
	if err := rcm.DownloadBootROMImages(dev, blobs); err != nil {
		fmt.Fprintf(os.Stderr, "\nSEQUENCE FAILED: %v\n", err)
		fmt.Fprintln(os.Stderr, ">> The index in the error is the 0-based image that failed.")
		os.Exit(1)
	}
	fmt.Printf("\nOK: bootROM accepted all %d images.\n", len(blobs))
	fmt.Println(">> Next: the device should advance toward the mb2 applet (nv3p) phase.")
	if cid, err := dev.ReadChipID(); err == nil {
		fmt.Printf(">> Device still enumerable in RCM (chipID 0x%s).\n", cid)
	} else {
		fmt.Printf(">> Device no longer answers the RCM control read (%v) — may have re-enumerated.\n", err)
	}
}

// magic returns the 4-byte ASCII magic if printable ("NVDA" for signed blobs),
// else a hex preview. BCTs may not carry the NVDA magic; this is informational.
func magic(b []byte) string {
	if len(b) < 4 {
		return "(<4 bytes)"
	}
	printable := true
	for _, c := range b[:4] {
		if c < 0x20 || c > 0x7e {
			printable = false
			break
		}
	}
	if printable {
		return fmt.Sprintf("%q", string(b[:4]))
	}
	return fmt.Sprintf("%02x%02x%02x%02x", b[0], b[1], b[2], b[3])
}
