// Package bringup performs the T264 (Thor) stage-1 RCM boot from a host: it sends
// the bootROM image chain over USB Recovery Mode and then bct_mem + the blob, so
// mb1 boots the payload and the device comes up as the initrd-flash ADB gadget.
// All images are sent verbatim (the RcmDownLoadImages path); reading the bct_mb1
// response on the same handle is what keeps mb1 alive between downloads.
//
// Input files (produced on a Linux x86_64 builder; the NVIDIA flash tools are
// i386/x86-64 and do not run on macOS arm64). From a WendyOS Jetson-Thor
// tegraflash bundle (e.g. a jetson-agx-thor .tegraflash-tar):
//
//  1. Extract the bundle:    tar xf <bundle>.tegraflash-tar -C bundle-x && cd bundle-x
//  2. Generate the RCM-boot images WITHOUT a device attached (board info comes
//     from the bundled boardvars.sh, so no USB is touched):
//
//       MACHINE=jetson-agx-thor-devkit-nvme-wendyos \
//       BOARDID=3834 FAB=400 BOARDSKU=0008 BOARDREV=G.5 CHIPREV=1 CHIP_SKU=00:00:00:A0 \
//         ./tegra264-flash-helper.sh --no-flash --rcm-boot -u "" -v "" --datafile "" \
//         rcmboot-flash.xml.in initrd-flash.img wendyos-image.ext4.simg
//
//     (-u/-v empty = the ODM-open zero-key path. BOARD* values come from the
//     bundle's boardvars.sh; adjust per board.)
//
// This writes ./rcmboot_blob/ containing the files this package consumes:
//
//	br_bct_BR.bct                              -> bct_br
//	mb1_t264_prod_aligned_sigheader.bin.encrypt    -> mb1
//	psc_bl1_t264_prod_aligned_sigheader.bin.encrypt -> psc_bl1
//	mb1_bct_MB1_sigheader.bct.encrypt          -> bct_mb1
//	membct_<N>_sigheader.bct.encrypt           -> bct_mem (N = on-board RAMCODE / 2;
//	                                              for RAMCODE 12 -> membct_6)
//	blob.bin                                   -> the ~171 MB mb2/UEFI/initrd payload
//
// These artifacts are class-level (not per-unit) for ODM-open devices, so they can
// be generated once per BSP in CI and shipped for the wendy CLI to replay.
package bringup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/rcm"
)

// Artifact file names within the rcm-boot directory.
const (
	FileBctBR     = "br_bct_BR.bct"
	FileMB1       = "mb1_t264_prod_aligned_sigheader.bin.encrypt"
	FilePSCBL1    = "psc_bl1_t264_prod_aligned_sigheader.bin.encrypt"
	FileBctMB1    = "mb1_bct_MB1_sigheader.bct.encrypt"
	FileBlob      = "blob.bin"
	DefaultMemBCT = "membct_6_sigheader.bct.encrypt"
)

// Options controls a stage-1 RCM boot.
type Options struct {
	// Dir holds the rcm-boot artifacts (the rcmboot_blob directory, or any dir
	// containing the files named above).
	Dir string
	// MemBCT is the membct filename to use; empty means DefaultMemBCT. The correct
	// one is selected by the on-board RAMCODE (RAMCODE/2); membct_6 fits RAMCODE 12.
	MemBCT string
	Out    io.Writer
}

// Run executes the stage-1 RCM boot. On success the device has booted the blob and
// is re-enumerating as the initrd-flash ADB gadget.
func Run(opts Options) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	memBCT := opts.MemBCT
	if memBCT == "" {
		memBCT = DefaultMemBCT
	}

	bctBR, err := read(opts.Dir, FileBctBR)
	if err != nil {
		return err
	}
	mb1, err := read(opts.Dir, FileMB1)
	if err != nil {
		return err
	}
	pscBL1, err := read(opts.Dir, FilePSCBL1)
	if err != nil {
		return err
	}
	bctMB1, err := read(opts.Dir, FileBctMB1)
	if err != nil {
		return err
	}
	bctMem, err := read(opts.Dir, memBCT)
	if err != nil {
		return err
	}
	blob, err := read(opts.Dir, FileBlob)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "Waiting for Jetson in USB recovery mode...")
	dev, err := rcm.WaitForDevice()
	if err != nil {
		return fmt.Errorf("waiting for device: %w", err)
	}
	defer dev.Close()
	fmt.Fprintf(out, "  device: %s\n", dev.String())

	fmt.Fprintln(out, "Sending bootROM images (bct_br, mb1, psc_bl1, bct_mb1)...")
	if err := rcm.DownloadBootROMImages(dev, [][]byte{bctBR, mb1, pscBL1, bctMB1}); err != nil {
		return fmt.Errorf("bootROM image sequence: %w", err)
	}
	// Read the bct_mb1 response (the mb1 version) on the same handle. This
	// completes the bootROM handshake and is what keeps mb1 alive for bct_mem/blob.
	if v := readResponse(dev); v != "" {
		fmt.Fprintf(out, "  mb1 up: %s\n", v)
	}

	fmt.Fprintf(out, "Sending bct_mem (%s, %d bytes)...\n", memBCT, len(bctMem))
	if err := dev.Write(bctMem); err != nil {
		return fmt.Errorf("sending bct_mem: %w", err)
	}
	readResponse(dev)

	fmt.Fprintf(out, "Sending blob (%d bytes; mb2/UEFI/initrd)...\n", len(blob))
	t0 := time.Now()
	if err := dev.Write(blob); err != nil {
		return fmt.Errorf("sending blob: %w", err)
	}
	fmt.Fprintf(out, "  blob sent in %v; mb1 is booting the payload.\n", time.Since(t0).Round(time.Millisecond))
	readResponse(dev)
	return nil
}

// readResponse does a tolerant bulk-IN read of any status the device returns and
// returns it as a printable string (empty if none / not printable).
func readResponse(dev *rcm.Device) string {
	buf := make([]byte, 512)
	n, err := dev.ReadWithTimeout(buf, 2*time.Second)
	if err != nil || n == 0 {
		return ""
	}
	out := make([]byte, 0, n)
	for _, c := range buf[:n] {
		if c == 0 {
			break
		}
		if c >= 0x20 && c < 0x7f {
			out = append(out, c)
		}
	}
	return string(out)
}

func read(dir, name string) ([]byte, error) {
	p := filepath.Join(dir, name)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading rcm-boot artifact %s: %w", name, err)
	}
	return data, nil
}
