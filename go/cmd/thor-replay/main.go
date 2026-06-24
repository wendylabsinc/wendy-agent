// Command thor-replay drives the T264 bootROM RCM download sequence using
// pre-generated artifact files supplied on the command line, in order. It is the
// "capture and replay" harness: generate the zerosign BCTs/blobs on a Linux host
// with real tegraflash, then replay them at a Thor from a Mac.
//
// Usage:
//
//	thor-replay <bct_br> <mb1> <psc_bl1> <bct_mb1> [...]
//
// It is a DIAGNOSTIC: it sends each blob verbatim and is tolerant of/loud about the
// per-image status read (a macOS bulk-IN quirk), so we can observe the whole sequence.
// It WRITES to the device. It does not attempt the later nv3p/applet phases.
package main

import (
	"fmt"
	"os"
	"time"

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
		fmt.Printf("  [%d] %-46s %8d bytes  magic=%s\n", i, p, len(data), magic(data))
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

	// Drain any connect-time bulk IN (e.g. ECID), tolerantly.
	pre := make([]byte, 512)
	if n, err := dev.Read(pre); err == nil {
		fmt.Printf("\ndrain: read %d bytes: % x\n", n, pre[:min(n, 16)])
	} else {
		fmt.Printf("\ndrain: (nothing / %v)\n", err)
	}

	fmt.Printf("\nSending %d images...\n", len(blobs))
	for i, blob := range blobs {
		t0 := time.Now()
		if err := dev.Write(blob); err != nil {
			fmt.Fprintf(os.Stderr, "  [%d] WRITE FAILED after %v: %v\n", i, time.Since(t0), err)
			os.Exit(1)
		}
		fmt.Printf("  [%d] wrote %d bytes in %v\n", i, len(blob), time.Since(t0).Round(time.Millisecond))

		// Status read into a full 512-byte (maxpacket) buffer, tolerant.
		status := make([]byte, 512)
		t1 := time.Now()
		n, rerr := dev.Read(status)
		if rerr != nil {
			fmt.Printf("      status: read error after %v: %v (continuing)\n", time.Since(t1).Round(time.Millisecond), rerr)
		} else {
			fmt.Printf("      status: %d bytes: % x\n", n, status[:min(n, 16)])
		}
	}

	fmt.Println("\nSequence sent. Re-checking device state...")
	if cid, err := dev.ReadChipID(); err == nil {
		fmt.Printf(">> Still answering RCM control read, chipID 0x%s (bootROM likely still active).\n", cid)
	} else {
		fmt.Printf(">> RCM control read now fails (%v) — device may have advanced/re-enumerated.\n", err)
	}
}

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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
