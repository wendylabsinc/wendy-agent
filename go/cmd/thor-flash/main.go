// Command thor-flash flashes a Jetson AGX Thor (T264) from a host over USB,
// end to end: stage-1 RCM boot (bootROM chain + bct_mem + blob) brings the device
// up as the initrd-flash ADB gadget, then stage-2 writes the partitions over ADB.
//
// It is a thin wrapper; the logic lives in internal/cli/tegraflash/{bringup,flasher}
// (and the rcm and adb transports). See the bringup package for how the input
// artifacts are produced on a Linux builder.
//
// Usage:
//
//	thor-flash --bundle <dir> [--adb-dir <dir>] [--membct <file>] [--bringup-only] [--flash-only]
//
// <dir> holds the generated RCM-boot artifacts (rcmboot_blob) and, for stage 2,
// the flash images and NVIDIA bootburn scripts. --adb-dir is a directory containing
// our `adb` shim (built from cmd/wendy-adb as `adb`); it is prepended to PATH so the
// stage-2 bootburn flasher uses our USB transport instead of a system adb.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/bringup"
	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/flasher"
)

func main() {
	bundle := flag.String("bundle", "", "directory with the RCM-boot artifacts and flash images (required)")
	adbDir := flag.String("adb-dir", "", "directory containing the `adb` shim (cmd/wendy-adb built as adb); prepended to PATH for stage 2")
	memBCT := flag.String("membct", "", "membct filename (default membct_6_sigheader.bct.encrypt; selected by on-board RAMCODE/2)")
	bringupOnly := flag.Bool("bringup-only", false, "stop after stage-1 RCM boot")
	flashOnly := flag.Bool("flash-only", false, "skip stage-1; the device is already the initrd-flash ADB gadget")
	flag.Parse()

	if *bundle == "" {
		fmt.Fprintln(os.Stderr, "error: --bundle <dir> is required")
		flag.Usage()
		os.Exit(2)
	}

	if !*flashOnly {
		fmt.Println("== Stage 1: RCM boot ==")
		if err := bringup.Run(bringup.Options{Dir: *bundle, MemBCT: *memBCT, Out: os.Stdout}); err != nil {
			fmt.Fprintf(os.Stderr, "stage 1 failed: %v\n", err)
			os.Exit(1)
		}
		if *bringupOnly {
			fmt.Println("\nStage 1 complete (--bringup-only).")
			return
		}
	}

	fmt.Println("\n== Stage 2: flash partitions over ADB ==")
	if err := flasher.Run(flasher.Options{BundleDir: *bundle, ADBDir: *adbDir, Out: os.Stdout}); err != nil {
		fmt.Fprintf(os.Stderr, "stage 2 failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nFlash complete.")
}
