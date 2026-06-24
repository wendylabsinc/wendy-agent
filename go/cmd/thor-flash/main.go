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
//	thor-flash --linux-stage1 <dir> --linux-stage2 <dir> --bundle <dir> \
//	           [--adb-dir <dir>] [--membct <file>] [--bringup-only] [--flash-only]
//
// Three inputs:
//   - --linux-stage1 <dir>: the RCM-boot artifacts generated on the Linux builder
//     (rcmboot_blob contents: blob.bin, bcts, mb1, membct_*). Stage 1 consumes these.
//   - --linux-stage2 <dir>: the generated "out" directory from the Linux builder
//     (holds flash_workspace/ with flash-images/FileToFlash.txt + signed images, and
//     its sibling tools/). Stage 2 runs bootburn with -P <dir>/flash_workspace.
//     See package flasher for how both stage artifacts are generated.
//   - --bundle <dir>: a local copy of the extracted tegraflash bundle, used only for
//     NVIDIA's bootburn scripts (unified_flash/tools/flashtools/bootburn).
//
// --adb-dir is a directory containing our wendy-shim (built from cmd/wendy-shim)
// deployed as adb, lsusb and timeout; it is prepended to PATH so the stage-2
// bootburn flasher uses our USB transport instead of the (missing/x86) host tools.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/bringup"
	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/flasher"
)

func main() {
	linuxStage1 := flag.String("linux-stage1", "", "directory with the RCM-boot artifacts (linux-stage1)")
	linuxStage2 := flag.String("linux-stage2", "", "generated 'out' directory from the Linux builder (contains flash_workspace/ + tools/)")
	bundle := flag.String("bundle", "", "local copy of the extracted tegraflash bundle (for bootburn scripts)")
	adbDir := flag.String("adb-dir", "", "directory containing the `adb` shim (cmd/wendy-shim deployed as adb/lsusb/timeout); prepended to PATH for stage 2")
	memBCT := flag.String("membct", "", "membct filename (default membct_6_sigheader.bct.encrypt; selected by on-board RAMCODE/2)")
	bringupOnly := flag.Bool("bringup-only", false, "stop after stage-1 RCM boot")
	flashOnly := flag.Bool("flash-only", false, "skip stage-1; the device is already the initrd-flash ADB gadget")
	flag.Parse()

	if !*flashOnly && *linuxStage1 == "" {
		fmt.Fprintln(os.Stderr, "error: --linux-stage1 <dir> is required (unless --flash-only)")
		flag.Usage()
		os.Exit(2)
	}
	if !*bringupOnly {
		if *linuxStage2 == "" || *bundle == "" {
			fmt.Fprintln(os.Stderr, "error: --linux-stage2 <dir> and --bundle <dir> are required (unless --bringup-only)")
			flag.Usage()
			os.Exit(2)
		}
	}

	if !*flashOnly {
		fmt.Println("== Stage 1: RCM boot ==")
		if err := bringup.Run(bringup.Options{Dir: *linuxStage1, MemBCT: *memBCT, Out: os.Stdout}); err != nil {
			fmt.Fprintf(os.Stderr, "stage 1 failed: %v\n", err)
			os.Exit(1)
		}
		if *bringupOnly {
			fmt.Println("\nStage 1 complete (--bringup-only).")
			return
		}
	}

	fmt.Println("\n== Stage 2: flash partitions over ADB ==")
	if err := flasher.Run(flasher.Options{BundleDir: *bundle, WorkspaceDir: *linuxStage2, ADBDir: *adbDir, Out: os.Stdout}); err != nil {
		fmt.Fprintf(os.Stderr, "stage 2 failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nFlash complete.")
}
