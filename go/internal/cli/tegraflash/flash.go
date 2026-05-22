package tegraflash

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/bundle"
	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/nv3p"
	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/rcm"
)

// ErrT264NotReady is returned when the bundle lacks pre-compiled T264 BCT/blob
// files. Run `wendy flash prepare-t264-bcts <bundle>` (requires a Linux host
// with tegrabct_v2/tegrahost_v2) to generate them, then re-run flash.
var ErrT264NotReady = fmt.Errorf("bundle missing pre-compiled T264 BCT/blob files: " +
	"run `wendy flash prepare-t264-bcts` on a Linux host first")

const DefaultSkipLarger = int64(64 * 1024 * 1024)

// FlashOptions controls a Jetson USB recovery flash from a tegraflash bundle.
type FlashOptions struct {
	BundlePath string
	XMLName    string
	FullEMMC   bool
	SkipLarger int64
	Out        io.Writer
}

// Flash writes QSPI partitions, and optionally eMMC partitions, from a WendyOS
// tegraflash bundle to a Jetson in USB recovery mode.
func Flash(opts FlashOptions) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	fmt.Fprintln(out, "Opening tegraflash bundle...")
	b, err := bundle.Open(opts.BundlePath)
	if err != nil {
		return fmt.Errorf("opening bundle: %w", err)
	}
	defer b.Close()

	xmlData, xmlName, err := resolveLayoutXML(b, opts.XMLName, opts.FullEMMC)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  Partition layout: %s\n", xmlName)

	layout, err := bundle.ParseLayout(xmlData)
	if err != nil {
		return fmt.Errorf("parsing partition XML: %w", err)
	}

	totalParts := countWritablePartitions(layout, opts.FullEMMC)
	fmt.Fprintf(out, "  Partitions to write: %d\n", totalParts)

	// Pre-load T264 firmware files before waiting for the device.
	// The T264 bootROM resets ~300 ms after USB enumeration if it receives no
	// valid nv3p command; loading files here (not inside flashT264) eliminates
	// the file-IO gap between interface claim and the first bulk write.
	var preT264 *t264PreloadedFiles
	if b.T264Ready() {
		if pf, err := loadT264Files(b); err == nil {
			preT264 = pf
		}
	}

	fmt.Fprintln(out, "\nPut the Jetson into USB recovery mode:")
	fmt.Fprintln(out, "  1. Hold the REC / Force Recovery button")
	fmt.Fprintln(out, "  2. Press and release RESET")
	fmt.Fprintln(out, "  3. Release REC after about 2 seconds")
	fmt.Fprintln(out, "\nWaiting for device in recovery mode (up to 60 s)...")

	tDetect := time.Now()
	dev, err := rcm.WaitForDevice()
	if err != nil {
		return fmt.Errorf("waiting for device: %w", err)
	}
	fmt.Fprintf(out, "  Device: %s (detected in %dms)\n", dev.String(), time.Since(tDetect).Milliseconds())

	if dev.IsT264() {
		return flashT264(out, dev, b, layout, opts, preT264)
	}
	return flashT234(out, dev, b, layout, opts)
}

// t264PreloadedFiles holds T264 Phase 1 firmware loaded before device open to
// minimise the gap between interface claim and the first nv3p command.
type t264PreloadedFiles struct {
	brBct, mb1, pscBl1, mb1Bct []byte
}

func loadT264Files(b *bundle.Bundle) (*t264PreloadedFiles, error) {
	brBct, err := b.BRBct()
	if err != nil {
		return nil, fmt.Errorf("reading BR BCT: %w", err)
	}
	mb1, err := b.MB1Bin()
	if err != nil {
		return nil, fmt.Errorf("reading MB1: %w", err)
	}
	pscBl1, err := b.PSCBL1Bin()
	if err != nil {
		return nil, fmt.Errorf("reading PSC BL1: %w", err)
	}
	mb1Bct, err := b.MB1Bct()
	if err != nil {
		return nil, fmt.Errorf("reading MB1 BCT: %w", err)
	}
	return &t264PreloadedFiles{brBct, mb1, pscBl1, mb1Bct}, nil
}

// flashT264 performs the complete T264 (Jetson Thor) USB recovery flash.
//
// Phase 1 (bootROM): upload bct_br + mb1 + psc_bl1 + bct_mb1 via nv3p.
// Phase 2 (MB1):     upload bct_mem + blob via nv3p command 0x2.
// Phase 3 (nv3p):    write QSPI/eMMC partitions.
func flashT264(out io.Writer, dev *rcm.Device, b *bundle.Bundle, layout *bundle.PartitionLayout, opts FlashOptions, preloaded *t264PreloadedFiles) error {
	if !b.T264Ready() {
		dev.Close()
		return ErrT264NotReady
	}

	files := preloaded
	if files == nil {
		var err error
		files, err = loadT264Files(b)
		if err != nil {
			dev.Close()
			return err
		}
	}

	// Do NOT read the UID via GET_DESCRIPTOR(STRING, 3) here.
	// On macOS, the bootROM STALLs that request (macOS already consumed it during
	// enumeration) and the STALL starts the bootROM's ~300 ms reset timer, causing
	// the subsequent nv3p bulk write to fail with kIOReturnNotAttached.
	// The UID is available from IOKit ("USB Serial Number") without USB traffic.

	// T264 bootROM speaks nv3p v3 directly at PID 0x7026 — use NewClientT264 (version=3,
	// 16-byte header) which matches what tegrarcm_v2 NvTegra3pSend sends.
	p1Client, err := nv3p.NewClientT264(dev)
	if err != nil {
		dev.Close()
		return fmt.Errorf("T264 Phase 1 nv3p client: %w", err)
	}

	// The T264 bootROM requires a GetPlatformInfo handshake before it will
	// accept any download commands (matches NvTegraRcmIsApplet in tegrarcm_v2).
	fmt.Fprintf(out, "T264 Phase 1: handshake with bootROM...\n")
	isApplet, err := p1Client.IsAppletT264()
	if err != nil {
		dev.Close()
		return fmt.Errorf("T264 Phase 1 handshake: %w", err)
	}
	if !isApplet {
		dev.Close()
		return fmt.Errorf("T264 Phase 1: bootROM not in applet mode")
	}
	fmt.Fprintf(out, "T264 Phase 1: uploading bootROM firmware via nv3p (t=0)...\n")
	t0 := time.Now()
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"bct_br", files.brBct},
		{"mb1", files.mb1},
		{"psc_bl1", files.pscBl1},
		{"bct_mb1", files.mb1Bct},
	} {
		fmt.Fprintf(out, "  %-10s %7d bytes  t=%dms... ", f.name, len(f.data), time.Since(t0).Milliseconds())
		if err := p1Client.DownloadT264File(f.name, f.data); err != nil {
			dev.Close()
			return fmt.Errorf("T264 Phase 1 %s: %w", f.name, err)
		}
		fmt.Fprintln(out, "OK")
	}
	fmt.Fprintln(out, "  Phase 1 complete; waiting for MB1...")
	dev.Close()
	time.Sleep(500 * time.Millisecond)

	// --- Phase 2: MB1 downloads ---
	nv3pDev, err := rcm.WaitForNv3p()
	if err != nil {
		return fmt.Errorf("waiting for MB1: %w", err)
	}
	defer nv3pDev.Close()
	fmt.Fprintln(out, "  MB1 interface ready")

	client, err := nv3p.NewClient(nv3pDev)
	if err != nil {
		return fmt.Errorf("opening MB1 session: %w", err)
	}

	// Poll until MB1 is ready to accept downloads.
	const pollAttempts = 30
	var mb1Ready bool
	for i := 0; i < pollAttempts; i++ {
		ready, err := client.PollMB1()
		if err == nil && ready {
			mb1Ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !mb1Ready {
		return fmt.Errorf("T264 MB1 did not become ready for downloads")
	}
	fmt.Fprintln(out, "  MB1 ready")

	ramGroup := b.RAMGroup()
	fmt.Fprintf(out, "  RAM group: %d\n", ramGroup)
	memBct, err := b.MemBct(ramGroup)
	if err != nil {
		return fmt.Errorf("reading memory BCT: %w", err)
	}
	blob, err := b.Blob()
	if err != nil {
		return fmt.Errorf("reading firmware blob: %w", err)
	}

	fmt.Fprintln(out, "T264 Phase 2: uploading MB1 firmware...")
	fmt.Fprintf(out, "  bct_mem: %d bytes... ", len(memBct))
	if err := client.DownloadT264File("bct_mem", memBct); err != nil {
		return fmt.Errorf("sending bct_mem: %w", err)
	}
	fmt.Fprintln(out, "OK")
	fmt.Fprintf(out, "  blob:    %d bytes... ", len(blob))
	if err := client.DownloadT264File("blob", blob); err != nil {
		return fmt.Errorf("sending blob: %w", err)
	}
	fmt.Fprintln(out, "OK")
	fmt.Fprintln(out, "  Phase 2 complete; waiting for recovery interface...")

	// After Phase 2 the device boots into a full recovery environment.
	// Re-connect for partition writes.
	nv3pDev.Close()
	time.Sleep(2 * time.Second)

	partDev, err := rcm.WaitForNv3p()
	if err != nil {
		return fmt.Errorf("waiting for partition flash interface: %w", err)
	}
	defer partDev.Close()

	partClient, err := nv3p.NewClient(partDev)
	if err != nil {
		return fmt.Errorf("opening partition flash session: %w", err)
	}

	info, err := partClient.GetPlatformInfo()
	if err != nil {
		return fmt.Errorf("getting platform info: %w", err)
	}
	fmt.Fprintf(out, "  Chip: 0x%04x  op_mode: 0x%x\n", info.ChipID.ID, info.OpMode)

	written, skipped, err := writePartitions(out, partClient, b, layout, opts.FullEMMC, opts.SkipLarger)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nFlash complete: %d partitions written, %d skipped.\n", written, skipped)
	if skipped > 0 && !opts.FullEMMC {
		fmt.Fprintln(out, "  Some partitions skipped; use eMMC flashing to write onboard eMMC partitions.")
	}
	fmt.Fprintln(out, "Resetting device...")
	_ = partClient.Reset()
	return nil
}

// flashT234 performs the T234 (Orin) USB recovery flash via DL_MINILOADER + nv3p.
func flashT234(out io.Writer, dev *rcm.Device, b *bundle.Bundle, layout *bundle.PartitionLayout, opts FlashOptions) error {
	applet, appletName, err := b.Applet()
	if err != nil {
		dev.Close()
		return fmt.Errorf("extracting applet: %w", err)
	}
	fmt.Fprintf(out, "  %s: %d bytes\n", appletName, len(applet))

	bctData, bctName, err := firstBCT(b, layout)
	if err != nil {
		fmt.Fprintf(out, "  BCT: none (will skip DlBCT)\n")
	} else {
		fmt.Fprintf(out, "  BCT: %s (%d bytes)\n", bctName, len(bctData))
	}

	// UID is sent by the bootROM on enumeration; on macOS the IOKit layer drops it
	// before the interface is claimed. It is informational only for ODM-open devices.
	uid, err := dev.ReadUID()
	if err != nil {
		fmt.Fprintf(out, "  UID: (unavailable: %v)\n", err)
	} else {
		fmt.Fprintf(out, "  UID: %x\n", uid)
	}

	fmt.Fprintln(out, "Loading applet via RCM...")
	if err := dev.LoadApplet(applet); err != nil {
		dev.Close()
		return fmt.Errorf("loading applet: %w", err)
	}
	fmt.Fprintln(out, "  Applet sent; waiting for nv3p interface...")
	dev.Close()
	time.Sleep(500 * time.Millisecond)

	nv3pDev, err := rcm.WaitForNv3p()
	if err != nil {
		return fmt.Errorf("waiting for nv3p: %w", err)
	}
	defer nv3pDev.Close()
	fmt.Fprintln(out, "  nv3p interface ready")

	client, err := nv3p.NewClient(nv3pDev)
	if err != nil {
		return fmt.Errorf("opening nv3p session: %w", err)
	}

	info, err := client.GetPlatformInfo()
	if err != nil {
		return fmt.Errorf("getting platform info: %w", err)
	}
	fmt.Fprintf(out, "  Chip: 0x%04x  op_mode: 0x%x\n", info.ChipID.ID, info.OpMode)

	if len(bctData) > 0 {
		fmt.Fprintf(out, "Loading BCT (%d bytes)...\n", len(bctData))
		if err := client.DlBCT(bctData); err != nil {
			return fmt.Errorf("loading BCT: %w", err)
		}
	}

	written, skipped, err := writePartitions(out, client, b, layout, opts.FullEMMC, opts.SkipLarger)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\nFlash complete: %d partitions written, %d skipped.\n", written, skipped)
	if skipped > 0 && !opts.FullEMMC {
		fmt.Fprintln(out, "  Some partitions skipped; use eMMC flashing to write onboard eMMC partitions.")
	}

	fmt.Fprintln(out, "Resetting device...")
	_ = client.Reset()
	return nil
}

func resolveLayoutXML(b *bundle.Bundle, xmlName string, fullEMMC bool) ([]byte, string, error) {
	if xmlName != "" {
		xmlData, err := b.ExtractFile(xmlName)
		if err != nil {
			return nil, "", fmt.Errorf("extracting XML %s: %w", xmlName, err)
		}
		return xmlData, xmlName, nil
	}
	xmlData, found, err := b.FindXML(fullEMMC)
	if err != nil {
		return nil, "", fmt.Errorf("finding partition XML: %w\n\nTip: use --tegraflash-xml <name> to specify the XML file", err)
	}
	return xmlData, found, nil
}

func countWritablePartitions(layout *bundle.PartitionLayout, fullEMMC bool) int {
	total := 0
	for i := range layout.Devices {
		dev := &layout.Devices[i]
		if !dev.IsQSPI() && (!fullEMMC || !dev.IsEMMC()) {
			continue
		}
		for j := range dev.Partitions {
			if dev.Partitions[j].HasFile() && !dev.Partitions[j].IsBCT() {
				total++
			}
		}
	}
	return total
}

func firstBCT(b *bundle.Bundle, layout *bundle.PartitionLayout) ([]byte, string, error) {
	for i := range layout.Devices {
		for j := range layout.Devices[i].Partitions {
			p := &layout.Devices[i].Partitions[j]
			if !p.IsBCT() || !p.HasFile() {
				continue
			}
			data, err := b.ExtractFile(p.Filename)
			if err != nil {
				return nil, "", fmt.Errorf("extracting BCT %s: %w", p.Filename, err)
			}
			return data, p.Filename, nil
		}
	}
	return nil, "", fmt.Errorf("BCT partition not found in XML; cannot initialise partition table")
}

func writePartitions(out io.Writer, client *nv3p.Client, b *bundle.Bundle, layout *bundle.PartitionLayout, fullEMMC bool, skipLarger int64) (int, int, error) {
	written := 0
	skipped := 0
	for i := range layout.Devices {
		devLayout := &layout.Devices[i]
		if devLayout.IsQSPI() {
			fmt.Fprintf(out, "\nWriting QSPI partitions (device %s instance %d):\n", devLayout.Type, devLayout.Instance)
		} else if devLayout.IsEMMC() && fullEMMC {
			fmt.Fprintf(out, "\nWriting eMMC partitions (device %s instance %d):\n", devLayout.Type, devLayout.Instance)
		} else {
			continue
		}

		for j := range devLayout.Partitions {
			p := &devLayout.Partitions[j]
			if !p.HasFile() || p.IsBCT() {
				continue
			}

			filename := strings.TrimSpace(p.Filename)
			partData, err := b.ExtractFile(filename)
			if err != nil {
				fmt.Fprintf(out, "  [SKIP] %s: file %q not in bundle (%v)\n", p.Name, filename, err)
				skipped++
				continue
			}

			if skipLarger > 0 && int64(len(partData)) > skipLarger {
				fmt.Fprintf(out, "  [SKIP] %s: %s is %d MB (exceeds limit %d MB)\n",
					p.Name, filename,
					int64(len(partData))/(1024*1024),
					skipLarger/(1024*1024))
				skipped++
				continue
			}

			fmt.Fprintf(out, "  Writing %-24s  %5d KB  (id=%d)... ", p.Name, len(partData)/1024, p.ID)
			if err := client.WritePartition(uint32(p.ID), 0x01, partData); err != nil {
				fmt.Fprintln(out, "FAILED")
				return written, skipped, fmt.Errorf("writing partition %s (id=%d): %w", p.Name, p.ID, err)
			}
			fmt.Fprintln(out, "OK")
			written++
		}
	}
	return written, skipped, nil
}
