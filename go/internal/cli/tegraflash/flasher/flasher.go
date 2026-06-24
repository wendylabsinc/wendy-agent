// Package flasher performs T264 (Thor) stage-2 flashing: once stage-1 RCM boot
// has brought the device up as the initrd-flash ADB gadget, it drives the
// device-side writes over ADB (self-contained, via internal/cli/tegraflash/adb —
// no external adb binary).
//
// The device-side model (from NVIDIA's bootburn flash_bsp_images.py /
// bootburn_adb.py) is:
//
//   - Push a small wrapper, wr_sh.sh, to /tmp. It runs `/bin/sh -c "$*"` and echoes
//     "EXITCODE=<n>" so the host can read the result of a shell command.
//   - Run commands as: adb shell /tmp/wr_sh.sh <command>   (AdbShell below).
//   - Push each partition image to /tmp, then write it to its target offset on the
//     internal storage (/dev/nvme0n1) and the QSPI boot device with dd/losetup/
//     blkdiscard, per the bundle's flash index (partition -> device/offset/file).
//
// Status: the connect + push + AdbShell enablers are implemented. The full
// partition-write loop (flash-index parsing + the per-partition dd sequence) is
// the remaining work; it mirrors bootburn_adb.py. A current gate is the device
// shell: on a bare RCM boot adbd's `shell:` service reports "fork failed" until the
// flashing environment is fully up (the real flow runs a provision/platform-detect
// step first) — see VerifyShell.
package flasher

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/adb"
)

// wrShWrapper is NVIDIA's tiny device-side shell wrapper: run the args via
// /bin/sh and report the exit code so the host can read command results.
const wrShWrapper = "#/bin/sh\n/bin/sh -c \"$*\"\nEXITCODE=$?\necho \"\"\necho \"EXITCODE=$EXITCODE\"\n"

const wrShPath = "/tmp/wr_sh.sh"

// Options controls stage-2 flashing.
type Options struct {
	// BundleDir holds the flash-images and tools (the generated flash workspace).
	BundleDir string
	Out       io.Writer
}

// Flasher is a stage-2 session over an ADB transport.
type Flasher struct {
	dev *adb.Device
	out io.Writer
}

// Connect waits for the initrd-flash ADB gadget to appear and opens it, then
// pushes the wr_sh.sh shell wrapper.
func Connect(out io.Writer) (*Flasher, error) {
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintln(out, "Waiting for the initrd-flash ADB gadget...")
	var dev *adb.Device
	var err error
	for i := 0; i < 30; i++ {
		dev, err = adb.Open()
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("connecting to ADB gadget: %w", err)
	}
	fmt.Fprintf(out, "  connected: %s\n", dev.Banner)

	f := &Flasher{dev: dev, out: out}
	if err := dev.Push([]byte(wrShWrapper), wrShPath, 0o755); err != nil {
		dev.Close()
		return nil, fmt.Errorf("pushing %s: %w", wrShPath, err)
	}
	return f, nil
}

// Close releases the transport.
func (f *Flasher) Close() {
	if f.dev != nil {
		f.dev.Close()
	}
}

// Shell runs a command on the device through wr_sh.sh and returns its output and
// exit code.
func (f *Flasher) Shell(command string) (output string, exit int, err error) {
	out, err := f.dev.Shell(wrShPath + " " + command)
	if err != nil {
		return out, -1, err
	}
	// wr_sh.sh appends a trailing "EXITCODE=<n>" line.
	exit = -1
	if i := strings.LastIndex(out, "EXITCODE="); i >= 0 {
		fields := strings.Fields(out[i+len("EXITCODE="):])
		if len(fields) > 0 {
			if n, e := strconv.Atoi(fields[0]); e == nil {
				exit = n
			}
		}
		out = out[:i]
	}
	return out, exit, nil
}

// Push copies local file data to remotePath on the device.
func (f *Flasher) Push(data []byte, remotePath string) error {
	return f.dev.Push(data, remotePath, 0o644)
}

// VerifyShell confirms the device shell is usable (exit 0 for a trivial command).
// On a bare RCM boot this can fail ("fork failed") until the flashing environment
// is fully up.
func (f *Flasher) VerifyShell() error {
	out, code, err := f.Shell("echo wendy-ok")
	if err != nil {
		return fmt.Errorf("shell transport error: %w", err)
	}
	if !strings.Contains(out, "wendy-ok") || code != 0 {
		return fmt.Errorf("device shell not ready (exit=%d, out=%q) — flashing environment not up yet", code, strings.TrimSpace(out))
	}
	fmt.Fprintln(f.out, "  device shell ready")
	return nil
}

// Run is the stage-2 entry point. It connects, verifies the shell, then performs
// the partition writes.
func Run(opts Options) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	f, err := Connect(out)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := f.VerifyShell(); err != nil {
		return err
	}

	// Report the device storage so we can confirm the flashing environment, then
	// write the partitions.
	if parts, _, err := f.Shell("cat /proc/partitions"); err == nil {
		fmt.Fprintf(out, "device partitions:\n%s\n", parts)
	}

	return f.flashPartitions(opts.BundleDir)
}

// flashPartitions writes the bundle's images to the internal storage and QSPI.
//
// TODO: implement the per-partition write loop. It mirrors bootburn_adb.py:
// parse the flash index (partition -> target device, offset, image file), and for
// each entry push the image to /tmp and dd it into place (with losetup/blkdiscard/
// resize2fs for the rootfs partitions), plus write the GPTs. The flash index and
// images live under the bundle's flash workspace (out/flash_workspace/flash-images).
func (f *Flasher) flashPartitions(bundleDir string) error {
	return fmt.Errorf("partition write loop not yet implemented (see flasher.flashPartitions; bundleDir=%s)", bundleDir)
}
