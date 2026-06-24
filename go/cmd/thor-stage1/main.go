// Command thor-stage1 drives a T264 through the full RCM-boot image sequence from
// a Mac: the four bootROM images, then bct_mem and the blob — all sent verbatim
// (the RcmDownLoadImages path), reading the device's status after each that
// answers. After the blob, mb1 boots the blob payload (mb2/UEFI/initrd).
//
// Usage:
//
//	thor-stage1 <bct_br> <mb1> <psc_bl1> <bct_mb1> [bct_mem] [blob]
//
// Reading the bct_mb1 response on the same handle is what keeps mb1 alive; bct_mem
// and blob are then more verbatim downloads on the same handle.
//
// Input files (produced on a Linux x86_64 builder; the flash tools are i386/x86-64
// and do not run on macOS arm64). From a WendyOS Jetson-Thor tegraflash bundle
// (e.g. jetson-agx-thor .tegraflash-tar):
//
//  1. Extract the bundle:    tar xf <bundle>.tegraflash-tar -C bundle-x && cd bundle-x
//  2. Generate the RCM-boot images WITHOUT a device attached (board info is taken
//     from the bundled boardvars.sh, so no USB is touched):
//
//       MACHINE=jetson-agx-thor-devkit-nvme-wendyos \
//       BOARDID=3834 FAB=400 BOARDSKU=0008 BOARDREV=G.5 CHIPREV=1 CHIP_SKU=00:00:00:A0 \
//         ./tegra264-flash-helper.sh --no-flash --rcm-boot -u "" -v "" --datafile "" \
//         rcmboot-flash.xml.in initrd-flash.img wendyos-image.ext4.simg
//
//     (-u/-v empty = the ODM-open zero-key path. BOARD* values come from the bundle's
//     boardvars.sh; adjust per board.)
//
// This writes ./rcmboot_blob/ containing the files this tool needs:
//
//	<bct_br>   = br_bct_BR.bct
//	<mb1>      = mb1_t264_prod_aligned_sigheader.bin.encrypt
//	<psc_bl1>  = psc_bl1_t264_prod_aligned_sigheader.bin.encrypt
//	<bct_mb1>  = mb1_bct_MB1_sigheader.bct.encrypt
//	<bct_mem>  = membct_<N>_sigheader.bct.encrypt   (N = on-board RAMCODE / 2;
//	             rcmbootcmd.txt selects it at flash time. For RAMCODE 12 → membct_6.)
//	<blob>     = blob.bin                            (~171 MB; mb2/UEFI/initrd payload)
//
// These artifacts are class-level (not per-unit) for ODM-open devices, so they can
// be generated once per BSP in CI and shipped for the wendy CLI to replay.
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/rcm"
)

func main() {
	if len(os.Args) < 5 || len(os.Args) > 7 {
		fmt.Fprintln(os.Stderr, "usage: thor-stage1 <bct_br> <mb1> <psc_bl1> <bct_mb1> [bct_mem] [blob]")
		os.Exit(2)
	}
	read := func(p string) []byte {
		d, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL reading %s: %v\n", p, err)
			os.Exit(1)
		}
		return d
	}
	bootImages := [][]byte{read(os.Args[1]), read(os.Args[2]), read(os.Args[3]), read(os.Args[4])}
	var bctMem, blob []byte
	if len(os.Args) >= 6 {
		bctMem = read(os.Args[5])
	}
	if len(os.Args) == 7 {
		blob = read(os.Args[6])
	}

	fmt.Println("Waiting for Jetson in recovery mode...")
	dev, err := rcm.WaitForDevice()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("device: %s isT264=%v\n", dev.String(), dev.IsT264())

	fmt.Println("\nSending 4 bootROM images...")
	if err := rcm.DownloadBootROMImages(dev, bootImages); err != nil {
		fmt.Fprintf(os.Stderr, "bootROM sequence FAILED: %v\n", err)
		os.Exit(1)
	}
	// Read the bct_mb1 status (mb1 version) on the same handle — completes the
	// handshake that keeps mb1 alive.
	readStatus(dev, "bct_mb1")

	// bct_mem and blob are the same verbatim RcmDownLoadImages path as the bootROM
	// images, continued on the same handle.
	if bctMem != nil {
		fmt.Printf("\nSending bct_mem (%d bytes) verbatim...\n", len(bctMem))
		if err := dev.Write(bctMem); err != nil {
			fmt.Fprintf(os.Stderr, ">> bct_mem write failed: %v\n", err)
			os.Exit(1)
		}
		readStatus(dev, "bct_mem")
	}
	if blob != nil {
		fmt.Printf("\nSending blob (%d bytes) verbatim... (this streams ~%d MiB)\n", len(blob), len(blob)/(1<<20))
		t0 := time.Now()
		if err := dev.Write(blob); err != nil {
			fmt.Fprintf(os.Stderr, ">> blob write failed after %v: %v\n", time.Since(t0).Round(time.Millisecond), err)
			os.Exit(1)
		}
		fmt.Printf(">> blob streamed in %v\n", time.Since(t0).Round(time.Millisecond))
		readStatus(dev, "blob")
	}
	dev.Close()

	if blob == nil {
		fmt.Println("\n(no blob given — pass bct_mem and blob to boot mb2)")
		return
	}

	// After the blob, mb1 boots it: the device should re-enumerate / advance.
	fmt.Println("\nblob sent — checking whether the device boots the payload...")
	time.Sleep(2 * time.Second)
	for i := 0; i < 20; i++ {
		ndev, err := rcm.WaitForNv3p()
		if err != nil {
			fmt.Printf("  [%d] device absent: %v\n", i, err)
			time.Sleep(time.Second)
			continue
		}
		cid, cerr := ndev.ReadChipID()
		fmt.Printf("  [%d] device present: %s chip-id=%s\n", i, ndev.String(), cidStr(cid, cerr))
		ndev.Close()
		time.Sleep(time.Second)
	}
	fmt.Println(">> watch for an ADB device (initrd-flash) to confirm stage 2.")
}

// readStatus does a tolerant bulk-IN read and prints what the device returned.
func readStatus(dev *rcm.Device, label string) {
	buf := make([]byte, 512)
	n, err := dev.ReadWithTimeout(buf, 2*time.Second)
	if err != nil {
		fmt.Printf("  %s status: (no response: %v)\n", label, err)
		return
	}
	fmt.Printf("  %s status: %d bytes (%q)\n", label, n, trimStr(buf[:n]))
	if n > 0 {
		fmt.Printf("    %s", hex.Dump(buf[:min(n, 32)]))
	}
}

func trimStr(b []byte) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		if c >= 0x20 && c < 0x7f {
			out = append(out, c)
		}
	}
	return string(out)
}

func cidStr(cid string, err error) string {
	if err != nil {
		return "(ctrl-read err)"
	}
	return "0x" + cid
}
