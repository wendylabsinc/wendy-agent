//go:build darwin || linux

// t264-usb-diag: T264 bootROM Phase 1 + MB1 Phase 2 download sequence.
//
// Protocol (from tegrarcm_v2 mainT23x binary analysis):
//
// Phase 1 (bootROM):
//  1. Open USB
//  2. GET_DESCRIPTOR(String, index=3) control transfer — reads UID, arms the bootROM session
//  3. Close USB, reopen USB
//  4. Send each file as raw bulk OUT writes (no nv3p framing), max 2048B per write:
//     a. bct_br
//     b. mb1
//     c. psc_bl1
//     d. bct_mb1
//  5. Close USB — device reboots into MB1 applet
//  6. Wait for NVIDIA device re-enumeration
//
// Phase 2 (MB1 applet raw bulk — tegrarcm state=5→8):
//  1. Open USB, read 68 bytes (MB1 version string), close USB        [state 5→6]
//  2. Open USB, send bct_mem (membct_0_sigheader.bct.encrypt) raw    [state 6→7]
//  3. Send blob (blob.bin) raw in same USB session, close USB        [state 7→8]
//
// Usage:
//
//	sudo /tmp/t264-usb-diag [-v] [-skip-uid] [-fresh-ctx] [-try-in-first] [-no-reopen] [-concurrent] [-timeout N] [/path/to/t264-flash-dir]
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/gousb"
	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/nv3p"
)

const (
	vendorNVIDIA = gousb.ID(0x0955)
	pidThor      = gousb.ID(0x7026)
)

func main() {
	flashDir := "/tmp/wendy-t264-flash"
	verbose := false
	skipUID := false
	freshCtx := false
	tryInFirst := false
	noReopen := false
	debugUSB := false
	concurrent := false  // submit IN read and OUT write simultaneously
	claimAfterUID := false // open device, GET_DESCRIPTOR, THEN claim interface
	resetDevice := false // after GET_DESCRIPTOR: release iface, reset device, reopen
	useNv3p := false     // use nv3p v3 protocol (IsAppletT264 + DownloadT264File) instead of raw bulk
	nv3pProbe := false   // send nv3p CMD with pre-submitted IN read; log exactly what comes back
	nv3pNoAck := false   // send nv3p CMD + DATA immediately, no waitACK between them
	dlProbe := false     // send DownloadT264File CMD for bct_br, wait 30s for any IN response
	zeroSend := 0        // send N zero bytes as very first write (tests content vs prior-writes hypothesis)
	sizeProbe := false   // probe which transfer sizes the device accepts (short vs full USB packets)
	p2NoUID := false        // skip GET_DESCRIPTOR in openMB1 (test if UID arm is needed for MB1)
	p2Probe := false        // Phase 2 verbose probe: pre-submit 60s IN, send one CMD, wait; skip normal poll
	p2SameSession := false  // keep Phase 2a USB session open for Phase 2b (no close/reopen between them)
	p2DelayMs := 200        // delay between Phase 2a close and Phase 2b open (ms)
	p2ClearHalt := false    // send CLEAR_FEATURE(ENDPOINT_HALT) to OUT EP before first write
	p2GetStatus := false    // read GET_STATUS on OUT/IN endpoints before first write; print halt bit
	p2SetInterface := false // send SET_INTERFACE(0,0) before first write (resets endpoint DATA toggle)
	p2DrainIn := false      // do an extra IN read (max 512B, 500ms timeout) before OUT writes
	p2SleepAfterInMs := 0        // sleep N ms after Phase 2a IN read, before Phase 2b OUT write
	p2LibusbClearHalt := false   // call libusb_clear_halt() (IOKit ClearPipeStall) on OUT endpoint
	p2LibusbSetAlt := false      // call libusb_set_interface_alt_setting() (IOKit SetAlternateInterface)
	p2SkipVersionRead := false   // skip Phase 2a 68-byte IN read; test if IN read triggers re-enum
	p2RetryCount := 0            // retry each Phase 2b chunk up to N times on transfer error (100ms between)
	p2ReconnectCount := 0        // on "no device" in Phase 2b: close, wait for re-enum, reopen, retry
	p2CgoWrite := false          // use libusb_bulk_transfer directly (bypass gousb) with 5000ms timeout
	p2Prewrite := false          // pre-submit bct_mem OUT write before IN read (requires -p2-same-session)
	p2AbortIn := false           // call libusb_clear_halt on IN ep before Phase 2b OUT writes
	p2RereadVersion := false     // re-read version string in Phase 2b session (drain SET_INTERFACE re-queue)
	p2Nv3p := false             // Phase 2b: use nv3p v3 (IsAppletT264 + DownloadT264File) instead of raw bulk
	p2Nv3pNoIsApplet := false   // skip IsAppletT264; go straight to DownloadT264File (for devices that don't respond to GetPlatformInfo)
	p2AsyncIn := false          // start async IN goroutine alongside raw bulk (keeps IN URBs pending; logs everything received)
	p2ChunkOverride := 0        // if non-zero, override default 16384-byte chunk size for Phase 2 writes
	p2ReadTimeoutSec := 15       // per-read timeout for Phase 2 IN reads
	p2ReadAfterChunk := false   // after each successful chunk write, do a blocking IN read before next write
	p2AfterChunkMs := 2000      // timeout (ms) for the per-chunk IN read (-p2-read-after-chunk)
	outTimeoutSec := 30

	args := os.Args[1:]
	for len(args) > 0 {
		switch args[0] {
		case "-v":
			verbose = true
		case "-skip-uid":
			skipUID = true
		case "-fresh-ctx":
			freshCtx = true
		case "-try-in-first":
			tryInFirst = true
		case "-no-reopen":
			noReopen = true
		case "-debug-usb":
			debugUSB = true
		case "-concurrent":
			concurrent = true
			noReopen = true
		case "-claim-after-uid":
			// Open device, GET_DESCRIPTOR on EP0 (no interface claim), THEN claim interface, THEN bulk OUT
			claimAfterUID = true
		case "-reset-device":
			// After GET_DESCRIPTOR: release interface, reset device, wait for re-enum, reopen
			resetDevice = true
		case "-nv3p":
			// Use nv3p v3 protocol: IsAppletT264 handshake then DownloadT264File per file
			useNv3p = true
			noReopen = true // keep same USB session for the full exchange
		case "-nv3p-probe":
			// Send nv3p v3 GetPlatformInfo CMD with pre-submitted IN goroutine;
			// poll IN after EACH write and log hex of everything received.
			nv3pProbe = true
			noReopen = true
		case "-nv3p-noack":
			// Send nv3p v3 DownloadT264File CMD then DATA immediately, skipping all
			// device-to-host ACK reads. Tests the "no ACK from device" protocol variant.
			nv3pNoAck = true
			noReopen = true
		case "-zero-send":
			// Send exactly N zero bytes as the first write (tests data-content hypothesis).
			if len(args) > 1 {
				if n, err := strconv.Atoi(args[1]); err == nil {
					zeroSend = n
					args = args[1:]
				}
			}
			noReopen = true
		case "-dl-probe":
			// Send DownloadT264File CMD for bct_br (cmd=2), pre-submit 30s IN goroutine,
			// and wait to see if device responds. Answers: does device ACK cmd=2?
			dlProbe = true
			noReopen = true
		case "-size-probe":
			// Probe which USB transfer sizes the device ACKs vs NAKs.
			// Tests 1..65536 byte writes to find any size limit.
			sizeProbe = true
			noReopen = true
		case "-p2-no-uid":
			p2NoUID = true
		case "-p2-same-session":
			// Keep Phase 2a USB session open for Phase 2b — no close/reopen.
			// Tests whether macOS IOKit rapid close/open is causing the transfer error.
			p2SameSession = true
		case "-p2-delay":
			if len(args) > 1 {
				if n, err := strconv.Atoi(args[1]); err == nil {
					p2DelayMs = n
					args = args[1:]
				}
			}
		case "-p2-clear-halt":
			// Send CLEAR_FEATURE(ENDPOINT_HALT) to OUT EP before first write.
			// Unstalls a halted OUT pipe on both host and device sides.
			p2ClearHalt = true
		case "-p2-get-status":
			// Read GET_STATUS for OUT and IN endpoints; print whether HALT bit is set.
			p2GetStatus = true
		case "-p2-set-interface":
			// Send SET_INTERFACE(0,0) before Phase 2b writes to reset endpoint DATA toggles.
			p2SetInterface = true
		case "-p2-drain-in":
			// Read up to 512B from IN with a 500ms timeout before first OUT write.
			// Drains any extra IN data MB1 may send after the 68-byte version string.
			p2DrainIn = true
		case "-p2-sleep-after-in":
			// Sleep N ms after Phase 2a IN read, before Phase 2b OUT write.
			// Tests whether MB1 needs time to transition from "TX version" to "RX files" state.
			if len(args) > 1 {
				if n, err := strconv.Atoi(args[1]); err == nil {
					p2SleepAfterInMs = n
					args = args[1:]
				}
			}
		case "-p2-libusb-clear-halt":
			// Call libusb_clear_halt() which invokes IOKit ClearPipeStall on the OUT endpoint.
			// Unlike -p2-clear-halt (USB CLEAR_FEATURE only), this also resets the host-side
			// IOKit pipe state to kIOUSBHostPipeStateOpen. Required when kIOReturnBusy (0xe00002ed)
			// is the transfer error after an IN read leaves the OUT pipe in a non-open state.
			p2LibusbClearHalt = true
		case "-p2-libusb-set-alt":
			// Call libusb_set_interface_alt_setting() which invokes IOKit SetAlternateInterface.
			// Resets ALL endpoint DATA toggles and pipe states on both host and device sides —
			// more thorough than -p2-libusb-clear-halt as it resets every pipe at once.
			p2LibusbSetAlt = true
		case "-p2-skip-version-read":
			// Skip the Phase 2a 68-byte BL version IN read entirely.
			// Tests whether the IN read itself triggers a device re-enumeration that
			// leaves the OUT endpoint in a non-responsive state for Phase 2b writes.
			p2SkipVersionRead = true
		case "-p2-retry":
			// Retry each Phase 2b bulk OUT chunk up to N times on transfer error,
			// with 100ms between each attempt. Addresses macOS XHCI timing out in
			// ~70ms (3 NAK retries) while the MB1 applet briefly NAKs after the IN
			// read as it transitions from "send version" to "receive files" state.
			// On Linux, USBDEVFS_BULK retries for 5s+ and succeeds; macOS does not.
			if len(args) > 1 {
				if n, err := strconv.Atoi(args[1]); err == nil {
					p2RetryCount = n
					args = args[1:]
				}
			}
		case "-p2-reconnect":
			// On "no device" during Phase 2b: close the stale handle, wait up to 30s
			// for the MB1 applet to re-enumerate, reopen, re-read the version string,
			// and retry all Phase 2b files from offset 0. Allows up to N reconnects.
			// Addresses the macOS behavior where the failed OUT transfer causes MB1 to
			// reset its USB stack ~170ms later; after re-enum the device may accept writes.
			if len(args) > 1 {
				if n, err := strconv.Atoi(args[1]); err == nil {
					p2ReconnectCount = n
					args = args[1:]
				}
			}
		case "-p2-cgo-write":
			// Use libusb_bulk_transfer() directly via CGo with a 5000ms timeout
			// instead of gousb's WriteContext. Tests whether gousb is computing a
			// shorter effective IOKit timeout than requested, or whether macOS XHCI
			// has its own built-in NAK limit that no application-level timeout can override.
			p2CgoWrite = true
		case "-p2-abort-in":
			// Call libusb_clear_halt on the IN endpoint before Phase 2b OUT writes.
			// Hypothesis: IOKit auto-submits a persistent IN polling loop when MB1 applet
			// has pending IN data (version string). This internal IN poll causes ep1 OUT
			// to return kIOReturnBusy. AbortPipe/ClearPipeStallBothEnds on the IN pipe
			// should cancel the auto-poll and allow OUT writes to proceed.
			p2AbortIn = true
		case "-p2-prewrite":
			// In -p2-same-session mode: pre-submit bct_mem[0:16384] as an OUT goroutine
			// BEFORE doing the Phase 2a IN read (version string). The OUT pipe is Open
			// at this point (after SetAlternateInterface); after the IN read completes,
			// IOKit may put the OUT pipe into Paused state. By having the OUT transfer
			// already in-flight through XHCI, it can complete even after the pipe goes
			// Paused — because XHCI hardware continuations bypass IOKit pipe state.
			p2Prewrite = true
			p2SameSession = true // implied
		case "-p2-reread-version":
			// After Phase 2b claims interface (Darwin sends SET_INTERFACE(0,0) on every
			// claim_interface call), MB1 applet may re-enter "send version string" state.
			// Read and discard the version string here before issuing OUT writes.
			p2RereadVersion = true
		case "-p2-nv3p":
			// Use nv3p v3 protocol for Phase 2b: IsAppletT264 handshake followed by
			// DownloadT264File for each file. MB1 applet speaks nv3p v3, not raw bulk.
			p2Nv3p = true
		case "-p2-nv3p-no-is-applet":
			// Skip IsAppletT264 (GetPlatformInfo); go straight to DownloadT264File.
			// Use when MB1 doesn't respond to GetPlatformInfo but does accept downloads.
			p2Nv3pNoIsApplet = true
			p2Nv3p = true // implied
		case "-p2-async-in":
			// Start asyncUSBTransport goroutine alongside raw bulk writes.
			// The goroutine continuously re-submits IN reads and logs all received data.
			// Hypothesis: kIOReturnNotResponding after IN read is caused by IOKit leaving
			// the OUT pipe non-open; a concurrent pending IN URB may prevent this.
			p2AsyncIn = true
		case "-chunk":
			// Override the default 16384-byte chunk size for Phase 2 writes.
			// Use with raw bulk to test if write size affects transfer success.
			if len(args) > 1 {
				if n, err := strconv.Atoi(args[1]); err == nil {
					p2ChunkOverride = n
					args = args[1:]
				}
			}
		case "-p2-probe":
			// After Phase 1, connect to MB1 at addr=2, pre-submit 60s IN goroutine,
			// send one GetPlatformInfo CMD, and wait for the result. Reports exact
			// hex of everything sent and received. Skips the normal polling loop.
			p2Probe = true
		case "-p2-read-timeout":
			if len(args) > 1 {
				if n, err := strconv.Atoi(args[1]); err == nil {
					p2ReadTimeoutSec = n
					args = args[1:]
				}
			}
		case "-p2-read-after-chunk":
			// After each successful chunk write, do a blocking IN read before the next write.
			// Tests whether MB1 sends a per-chunk ACK at the 8192-byte boundary and whether
			// reading it unblocks subsequent OUT writes.
			p2ReadAfterChunk = true
		case "-p2-after-chunk-ms":
			if len(args) > 1 {
				if n, err := strconv.Atoi(args[1]); err == nil {
					p2AfterChunkMs = n
					args = args[1:]
				}
			}
		case "-timeout":
			if len(args) > 1 {
				if n, err := strconv.Atoi(args[1]); err == nil {
					outTimeoutSec = n
					args = args[1:]
				}
			}
		default:
			flashDir = args[0]
		}
		args = args[1:]
	}

	fmt.Println("=== t264-usb-diag (bootROM Phase 1) ===")
	fmt.Printf("  skip-uid=%v  fresh-ctx=%v  try-in-first=%v  no-reopen=%v  concurrent=%v  timeout=%ds  debug-usb=%v  claim-after-uid=%v  reset-device=%v  nv3p=%v  nv3p-probe=%v  nv3p-noack=%v  dl-probe=%v  zero-send=%d  size-probe=%v  p2-no-uid=%v  p2-probe=%v  p2-same-session=%v  p2-delay=%dms  p2-clear-halt=%v  p2-get-status=%v  p2-set-interface=%v  p2-drain-in=%v  p2-sleep-after-in=%dms  p2-libusb-clear-halt=%v  p2-libusb-set-alt=%v  p2-skip-version-read=%v  p2-retry=%d  p2-reconnect=%d  p2-cgo-write=%v  p2-prewrite=%v  p2-abort-in=%v  p2-reread-version=%v  p2-nv3p=%v  p2-nv3p-no-is-applet=%v  p2-async-in=%v  p2-read-timeout=%ds\n",
		skipUID, freshCtx, tryInFirst, noReopen, concurrent, outTimeoutSec, debugUSB, claimAfterUID, resetDevice, useNv3p, nv3pProbe, nv3pNoAck, dlProbe, zeroSend, sizeProbe, p2NoUID, p2Probe, p2SameSession, p2DelayMs, p2ClearHalt, p2GetStatus, p2SetInterface, p2DrainIn, p2SleepAfterInMs, p2LibusbClearHalt, p2LibusbSetAlt, p2SkipVersionRead, p2RetryCount, p2ReconnectCount, p2CgoWrite, p2Prewrite, p2AbortIn, p2RereadVersion, p2Nv3p, p2Nv3pNoIsApplet, p2AsyncIn, p2ReadTimeoutSec)

	type dlFile struct {
		typeName string
		path     string
	}
	files := []dlFile{
		{"bct_br", flashDir + "/br_bct_BR.bct"},
		{"mb1", flashDir + "/mb1_t264_prod_aligned_sigheader.bin.encrypt"},
		{"psc_bl1", flashDir + "/psc_bl1_t264_prod_aligned_sigheader.bin.encrypt"},
		{"bct_mb1", flashDir + "/mb1_bct_MB1_sigheader.bct.encrypt"},
	}

	payloads := make([][]byte, len(files))
	for i, f := range files {
		raw, err := os.ReadFile(f.path)
		if err != nil {
			fmt.Printf("Cannot read %s (%s): %v\n", f.typeName, f.path, err)
			os.Exit(1)
		}
		payloads[i] = raw
		fmt.Printf("  %-10s %7d bytes  magic=%s\n",
			f.typeName+":", len(raw), hex.EncodeToString(raw[:4]))
	}

	// Phase 2 files (MB1 applet downloads via nv3p v3).
	// membct_0 is the RAMCODE=0 default; blob.bin is the main firmware image.
	type p2File struct {
		typeName string
		path     string
	}
	phase2Files := []p2File{
		{"bct_mem", flashDir + "/membct_0_sigheader.bct.encrypt"},
		{"blob", flashDir + "/blob.bin"},
	}
	phase2Payloads := make([][]byte, len(phase2Files))
	fmt.Println("Loading Phase 2 files...")
	for i, f := range phase2Files {
		raw, err := os.ReadFile(f.path)
		if err != nil {
			fmt.Printf("Cannot read Phase 2 file %s (%s): %v\n", f.typeName, f.path, err)
			os.Exit(1)
		}
		phase2Payloads[i] = raw
		fmt.Printf("  %-10s %7d bytes  magic=%s\n",
			f.typeName+":", len(raw), hex.EncodeToString(raw[:4]))
	}

	usbCtx := gousb.NewContext()
	defer usbCtx.Close()
	if debugUSB {
		usbCtx.Debug(4)
	}

	type usbHandles struct {
		dev   *gousb.Device
		iface *gousb.Interface
		done  func()
		outEP *gousb.OutEndpoint
		inEP  *gousb.InEndpoint
	}

	openDevice := func(ctx *gousb.Context) usbHandles {
		dev, err := ctx.OpenDeviceWithVIDPID(vendorNVIDIA, pidThor)
		if err != nil || dev == nil {
			fmt.Printf("Device not found: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Found: %s\n", dev.String())

		iface, done, err := dev.DefaultInterface()
		if err != nil {
			fmt.Printf("DefaultInterface: %v\n", err)
			os.Exit(1)
		}

		var outEP *gousb.OutEndpoint
		var inEP *gousb.InEndpoint
		for _, ep := range iface.Setting.Endpoints {
			if ep.TransferType == gousb.TransferTypeBulk {
				if ep.Direction == gousb.EndpointDirectionOut && outEP == nil {
					outEP, _ = iface.OutEndpoint(int(ep.Number))
				} else if ep.Direction == gousb.EndpointDirectionIn && inEP == nil {
					inEP, _ = iface.InEndpoint(int(ep.Number))
				}
			}
		}
		if outEP == nil {
			fmt.Println("Missing bulk OUT endpoint")
			os.Exit(1)
		}
		fmt.Printf("Bulk OUT: ep%d  maxPkt=%d", outEP.Desc.Number, outEP.Desc.MaxPacketSize)
		if inEP != nil {
			fmt.Printf("  Bulk IN: ep%d  maxPkt=%d", inEP.Desc.Number, inEP.Desc.MaxPacketSize)
		}
		fmt.Println()
		return usbHandles{dev, iface, done, outEP, inEP}
	}

	fmt.Printf("\nLooking for T264 (0x%04x:0x%04x)...\n", uint16(vendorNVIDIA), uint16(pidThor))

	var h usbHandles

	if claimAfterUID {
		// Open device without claiming interface, do GET_DESCRIPTOR, then claim interface.
		// Tests whether USBInterfaceOpen (= libusb_claim_interface) must come after the bootROM
		// session is armed by GET_DESCRIPTOR — matching how tegrarcm_v2 likely operates on Linux.
		fmt.Println("\n=== claim-after-uid: GET_DESCRIPTOR before interface claim ===")
		dev, err := usbCtx.OpenDeviceWithVIDPID(vendorNVIDIA, pidThor)
		if err != nil || dev == nil {
			fmt.Printf("Device not found: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Found (pre-claim): %s\n", dev.String())

		uidBuf := make([]byte, 130)
		n, err := dev.Control(0x80, 0x06, 0x0303, 0x0000, uidBuf)
		if err != nil {
			fmt.Printf("UID GET_DESCRIPTOR (pre-claim) failed: %v\n", err)
		} else {
			fmt.Printf("UID descriptor (%d bytes): %s\n", n, hex.EncodeToString(uidBuf[:n]))
			if verbose && n > 2 {
				fmt.Printf("  raw: %s\n", hex.Dump(uidBuf[:n]))
			}
		}

		fmt.Println("Claiming interface AFTER GET_DESCRIPTOR...")
		iface, done, err := dev.DefaultInterface()
		if err != nil {
			fmt.Printf("DefaultInterface (post-UID): %v\n", err)
			os.Exit(1)
		}
		var outEP *gousb.OutEndpoint
		var inEP *gousb.InEndpoint
		for _, ep := range iface.Setting.Endpoints {
			if ep.TransferType == gousb.TransferTypeBulk {
				if ep.Direction == gousb.EndpointDirectionOut && outEP == nil {
					outEP, _ = iface.OutEndpoint(int(ep.Number))
				} else if ep.Direction == gousb.EndpointDirectionIn && inEP == nil {
					inEP, _ = iface.InEndpoint(int(ep.Number))
				}
			}
		}
		if outEP == nil {
			fmt.Println("Missing bulk OUT endpoint")
			os.Exit(1)
		}
		fmt.Printf("Bulk OUT: ep%d  maxPkt=%d", outEP.Desc.Number, outEP.Desc.MaxPacketSize)
		if inEP != nil {
			fmt.Printf("  Bulk IN: ep%d  maxPkt=%d", inEP.Desc.Number, inEP.Desc.MaxPacketSize)
		}
		fmt.Println()
		h = usbHandles{dev, iface, done, outEP, inEP}
	} else if skipUID {
		fmt.Println("\n=== Skipping UID read — going straight to bulk OUT ===")
		h = openDevice(usbCtx)
	} else {
		fmt.Println("\n=== Reading UID (GET_DESCRIPTOR String index=3) ===")
		h = openDevice(usbCtx)

		uidBuf := make([]byte, 130)
		n, err := h.dev.Control(
			0x80,   // bmRequestType: device-to-host, standard, device
			0x06,   // bRequest: GET_DESCRIPTOR
			0x0303, // wValue: String descriptor, index 3
			0x0000, // wIndex: language 0
			uidBuf,
		)
		if err != nil {
			fmt.Printf("UID GET_DESCRIPTOR failed: %v\n", err)
		} else {
			fmt.Printf("UID descriptor (%d bytes): %s\n", n, hex.EncodeToString(uidBuf[:n]))
			if verbose && n > 2 {
				fmt.Printf("  raw: %s\n", hex.Dump(uidBuf[:n]))
			}
		}

		if tryInFirst && h.inEP != nil {
			fmt.Println("=== Probing IN endpoint (5s timeout) ===")
			inBuf := make([]byte, 512)
			inCtx, inCancel := context.WithTimeout(context.Background(), 5*time.Second)
			t0 := time.Now()
			n2, err2 := h.inEP.ReadContext(inCtx, inBuf)
			inCancel()
			fmt.Printf("IN probe: %d bytes in %.1fms err=%v  data=%s\n",
				n2, float64(time.Since(t0).Microseconds())/1000, err2, hex.EncodeToString(inBuf[:n2]))
		}

		if resetDevice {
			// Release interface, send USB reset, wait for re-enumeration, reopen.
			// Tests whether a SET_INTERFACE-free reopen (after USB Reset) changes NAK behaviour.
			fmt.Println("=== reset-device: releasing iface + USB reset + reopen ===")
			h.done()
			if err := h.dev.Reset(); err != nil {
				fmt.Printf("USB Reset failed: %v\n", err)
			} else {
				fmt.Println("USB Reset sent.")
			}
			h.dev.Close()
			fmt.Print("Waiting for device after reset")
			disappeared := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
				d, _ := usbCtx.OpenDeviceWithVIDPID(vendorNVIDIA, pidThor)
				if d == nil {
					if !disappeared {
						fmt.Print(" [disappeared]")
						disappeared = true
					}
				} else {
					d.Close()
					if disappeared {
						fmt.Print(" [reappeared]")
					} else {
						fmt.Print(" [still present]")
					}
					fmt.Println()
					break
				}
			}
			if !disappeared {
				fmt.Println(" (no USB reset detected)")
			}
			time.Sleep(200 * time.Millisecond)
			var bulkCtx *gousb.Context
			if freshCtx {
				fmt.Println("=== Creating fresh gousb context for bulk session ===")
				bulkCtx = gousb.NewContext()
				defer bulkCtx.Close()
			} else {
				bulkCtx = usbCtx
			}
			h = openDevice(bulkCtx)
		} else if noReopen {
			fmt.Println("\n=== no-reopen: reusing same USB session for bulk writes ===")
		} else {
			h.done()
			h.dev.Close()
			fmt.Print("Waiting for device after UID read")
			disappeared := false
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
				d, _ := usbCtx.OpenDeviceWithVIDPID(vendorNVIDIA, pidThor)
				if d == nil {
					if !disappeared {
						fmt.Print(" [disappeared]")
						disappeared = true
					}
				} else {
					d.Close()
					if disappeared {
						fmt.Print(" [reappeared]")
					} else {
						fmt.Print(" [still present]")
					}
					fmt.Println()
					break
				}
			}
			if !disappeared {
				fmt.Println(" (no USB reset detected)")
			}
			time.Sleep(100 * time.Millisecond)

			var bulkCtx *gousb.Context
			if freshCtx {
				fmt.Println("=== Creating fresh gousb context for bulk session ===")
				bulkCtx = gousb.NewContext()
				defer bulkCtx.Close()
			} else {
				bulkCtx = usbCtx
			}
			h = openDevice(bulkCtx)
		}
	}

	// Step 2: Send Phase 1 files.
	fmt.Println("\n=== Phase 1: bct_br → mb1 → psc_bl1 → bct_mb1 ===")

	if zeroSend > 0 {
		// Send exactly N zero bytes as the first OUT write after UID.
		// If this hangs (like bct_br), data content is NOT the differentiator.
		// If this succeeds instantly (like size-probe small writes), test whether
		// bct_br data succeeds as the SECOND write.
		fmt.Printf("\n=== zero-send: writing %d zero bytes ===\n", zeroSend)
		buf := make([]byte, zeroSend)
		t0 := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		n, err := h.outEP.WriteContext(ctx, buf)
		cancel()
		elapsed := time.Since(t0)
		if err != nil {
			fmt.Printf("  FAILED (%d written) in %.3fs: %v\n", n, elapsed.Seconds(), err)
			h.done(); h.dev.Close()
			return
		}
		fmt.Printf("  OK (%d bytes) in %.3fs\n", n, elapsed.Seconds())

		// Now try the first 2048B of bct_br to test if a prior write unlocks the device.
		fmt.Printf("  Now trying first 2048B of bct_br after the zero write...\n")
		data := payloads[0]
		chunkEnd := 2048
		if chunkEnd > len(data) { chunkEnd = len(data) }
		t0 = time.Now()
		ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
		n2, err2 := h.outEP.WriteContext(ctx2, data[:chunkEnd])
		cancel2()
		elapsed2 := time.Since(t0)
		if err2 != nil {
			fmt.Printf("  bct_br[0:2048] FAILED (%d written) in %.3fs: %v\n", n2, elapsed2.Seconds(), err2)
		} else {
			fmt.Printf("  bct_br[0:2048] OK (%d bytes) in %.3fs\n", n2, elapsed2.Seconds())
		}
		h.done(); h.dev.Close()
		return
	}

	if dlProbe {
		// dl-probe: pre-submit a 30s IN goroutine, send DownloadT264File CMD for bct_br,
		// then wait for any IN response. Determines whether device ACKs cmd=2 (unlike cmd=1
		// GetPlatformInfo which we already confirmed gets no response).
		if h.inEP == nil {
			fmt.Println("dl-probe requires bulk IN endpoint")
			h.done(); h.dev.Close(); os.Exit(1)
		}

		type inResult struct {
			n   int
			err error
			buf []byte
		}
		presub := make(chan inResult, 1)
		go func() {
			buf := make([]byte, 256)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			n, err := h.inEP.ReadContext(ctx, buf)
			presub <- inResult{n, err, buf[:n]}
		}()
		fmt.Println("Pre-submitted IN goroutine (30s timeout).")

		data := payloads[0] // bct_br
		fmt.Printf("Sending DownloadT264File CMD for bct_br (%d bytes)...\n", len(data))

		mkHdr := func(pktType, seq uint32) []byte {
			b := make([]byte, 16)
			binary.LittleEndian.PutUint32(b[0:], 3)
			binary.LittleEndian.PutUint32(b[4:], pktType)
			binary.LittleEndian.PutUint32(b[8:], seq)
			return b
		}

		args := make([]byte, 56)
		binary.LittleEndian.PutUint64(args[0:], uint64(len(data)))
		copy(args[16:], "bct_br")

		hdr := mkHdr(1, 0)
		sizeField := make([]byte, 4)
		binary.LittleEndian.PutUint32(sizeField, uint32(len(args)))
		cmdField := make([]byte, 4)
		binary.LittleEndian.PutUint32(cmdField, 2)
		cmdArgs := append(cmdField, args...)

		// v3 checksum: sum(header) + sum(cmd+args), no sizeField
		var ck uint32
		for _, b := range hdr     { ck += uint32(b) }
		for _, b := range cmdArgs { ck += uint32(b) }
		cs := make([]byte, 4)
		binary.LittleEndian.PutUint32(cs, ^ck+1)

		writes := []struct{ label string; buf []byte }{
			{"CMD header (16B)", hdr},
			{"CMD size   (4B)", sizeField},
			{"CMD cmd+args (60B)", cmdArgs},
			{"CMD checksum (4B)", cs},
		}
		failed := false
		for _, w := range writes {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := h.outEP.WriteContext(ctx, w.buf)
			cancel()
			if err != nil {
				fmt.Printf("  OUT %s: FAILED: %v\n", w.label, err)
				failed = true
				break
			}
			fmt.Printf("  OUT %s: OK\n", w.label)
		}

		if !failed {
			fmt.Println("\nCMD sent. Waiting up to 30s for IN response from device...")
			res := <-presub
			if res.n > 0 {
				fmt.Printf("Device responded: %d bytes = %s\n", res.n, hex.EncodeToString(res.buf))
			} else {
				fmt.Printf("No IN data in 30s (err=%v) — device does not ACK cmd=2 (DownloadT264File)\n", res.err)
			}
		} else {
			// drain goroutine
			go func() { <-presub }()
		}

		h.done(); h.dev.Close()
		return
	}

	if sizeProbe {
		// Probe which USB transfer sizes the T264 bootROM accepts (ACKs) vs rejects (NAKs/stalls).
		// Sends zeroed buffers of increasing sizes; logs OK, FAIL, and elapsed time for each.
		// Also reads IN for 300ms after each write to catch any device response.
		// The elapsed time reveals whether FAIL is an immediate STALL or a 5s timeout (NAK).
		testSizes := []int{1, 4, 8, 16, 32, 64, 128, 192, 255, 256, 384, 510, 511, 512, 513, 768, 1024, 2048, 4096, 8192, 16384, 65536}
		t0all := time.Now()
		fmt.Printf("\n=== size-probe: testing %d transfer sizes (zeroed data) ===\n", len(testSizes))
		for _, sz := range testSizes {
			buf := make([]byte, sz)
			t0 := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			n, err := h.outEP.WriteContext(ctx, buf)
			cancel()
			elapsed := time.Since(t0)
			totalElapsed := time.Since(t0all)
			if err != nil {
				fmt.Printf("  T=%5.1fs  OUT %5d bytes: FAILED (%d written) in %.3fs: %v\n",
					totalElapsed.Seconds(), sz, n, elapsed.Seconds(), err)
				fmt.Println("  Stopping after first failure.")
				break
			}
			fmt.Printf("  T=%5.1fs  OUT %5d bytes: OK in %.3fs\n",
				totalElapsed.Seconds(), sz, elapsed.Seconds())

			// Brief IN poll — catch any device ACK/NACK/response.
			if h.inEP != nil {
				rBuf := make([]byte, 64)
				rCtx, rCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
				rn, _ := h.inEP.ReadContext(rCtx, rBuf)
				rCancel()
				if rn > 0 {
					fmt.Printf("             IN %d bytes: %s\n", rn, hex.EncodeToString(rBuf[:rn]))
				}
			}
		}
		h.done()
		h.dev.Close()
		return
	}

	if nv3pNoAck {
		// No-ACK variant: send nv3p v3 CMD + DATA without any device-to-host ACK reads.
		// Tests the hypothesis that the T264 bootROM uses nv3p framing but never sends ACKs.
		if h.inEP == nil {
			fmt.Println("nv3p-noack requires bulk endpoints")
			h.done(); h.dev.Close(); os.Exit(1)
		}

		// Helper: send buf as one bulk OUT write.
		outWrite := func(label string, buf []byte) bool {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			n, err := h.outEP.WriteContext(ctx, buf)
			cancel()
			fmt.Printf("  OUT %-22s %d bytes: ", label, len(buf))
			if err != nil {
				fmt.Printf("FAILED (%d written): %v\n", n, err)
				return false
			}
			fmt.Printf("OK\n")
			return true
		}

		// Helper: build nv3p v3 header (16 bytes)
		mkHdr := func(pktType, seq uint32) []byte {
			b := make([]byte, 16)
			binary.LittleEndian.PutUint32(b[0:], 3) // version=3
			binary.LittleEndian.PutUint32(b[4:], pktType)
			binary.LittleEndian.PutUint32(b[8:], seq)
			return b
		}
		// Checksum: two's complement of sum of header bytes + body bytes (no size field in v3)
		mkChecksum := func(parts ...[]byte) []byte {
			var s uint32
			for _, p := range parts {
				for _, b := range p { s += uint32(b) }
			}
			cs := make([]byte, 4)
			binary.LittleEndian.PutUint32(cs, ^s+1)
			return cs
		}

		// Process each Phase 1 file with CMD+DATA, no ACK between.
	noackLoop:
		for i, f := range files {
			data := payloads[i]
			fmt.Printf("\n=== nv3p no-ACK: %s (%d bytes) ===\n", f.typeName, len(data))

			// Build DownloadT264File CMD args (56 bytes)
			args := make([]byte, 56)
			binary.LittleEndian.PutUint64(args[0:], uint64(len(data)))
			copy(args[16:], f.typeName)

			hdr := mkHdr(1, uint32(i*2)) // type=CMD, seq=i*2
			sizeField := make([]byte, 4)
			binary.LittleEndian.PutUint32(sizeField, uint32(len(args))) // args_len=56
			cmdField := make([]byte, 4)
			binary.LittleEndian.PutUint32(cmdField, 2) // CmdDownloadT264=2
			cmdArgs := append(cmdField, args...)
			cs := mkChecksum(hdr, cmdArgs) // v3: no size field in checksum

			// Send CMD as 4 separate writes (matching tegrarcm_v2 NvTegra3pSend)
			if !outWrite("CMD header", hdr) { break noackLoop }
			if !outWrite("CMD size", sizeField) { break noackLoop }
			if !outWrite("CMD cmd+args", cmdArgs) { break noackLoop }
			if !outWrite("CMD checksum", cs) { break noackLoop }

			// Poll IN briefly — expecting RESPONSE_CMD (type=9)
			{
				rCtx, rCancel := context.WithTimeout(context.Background(), 2*time.Second)
				rBuf := make([]byte, 64)
				rn, _ := h.inEP.ReadContext(rCtx, rBuf)
				rCancel()
				if rn > 0 {
					fmt.Printf("  IN after CMD: %d bytes = %s\n", rn, hex.EncodeToString(rBuf[:rn]))
				} else {
					fmt.Printf("  IN after CMD: 0 bytes (no response — proceeding without ACK)\n")
				}
			}

			// Send DATA packet (type=2, seq=i*2+1), no prior ACK from device.
			dataHdr := mkHdr(2, uint32(i*2+1))
			dataSz := make([]byte, 4)
			binary.LittleEndian.PutUint32(dataSz, uint32(len(data)))
			dataCS := mkChecksum(dataHdr, data) // v3: no size field

			if !outWrite("DATA header", dataHdr) { break noackLoop }
			if !outWrite("DATA size", dataSz) { break noackLoop }
			// Send data in 2048-byte chunks (T264 bootROM limit).
			for off := 0; off < len(data); {
				n := 2048
				if n > len(data)-off { n = len(data)-off }
				if !outWrite(fmt.Sprintf("DATA[%d:%d]", off, off+n), data[off:off+n]) { break noackLoop }
				off += n
			}
			if !outWrite("DATA checksum", dataCS) { break noackLoop }

			// Poll IN after DATA — expecting STATUS_CMD (type=8)
			{
				rCtx, rCancel := context.WithTimeout(context.Background(), 5*time.Second)
				rBuf := make([]byte, 64)
				rn, rerr := h.inEP.ReadContext(rCtx, rBuf)
				rCancel()
				fmt.Printf("  IN after DATA: %d bytes err=%v data=%s\n",
					rn, rerr, hex.EncodeToString(rBuf[:rn]))
			}
		}

		h.done(); h.dev.Close()
		fmt.Println("\n=== nv3p-noack done — waiting for re-enumeration (15s) ===")
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			devs, _ := usbCtx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
				return desc.Vendor == gousb.ID(vendorNVIDIA)
			})
			if len(devs) > 0 {
				for _, d := range devs {
					fmt.Printf("  Found NVIDIA pid=0x%04x\n", uint16(d.Desc.Product))
					d.Close()
				}
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Println("No NVIDIA device found after 15s")
		return
	}

	if nv3pProbe {
		// Probe: pre-submit IN read goroutine, then send nv3p v3 GetPlatformInfo CMD
		// as 4 separate writes (matching tegrarcm_v2 NvTegra3pSend).
		// Also poll IN after each individual write for 500ms.
		if h.inEP == nil {
			fmt.Println("nv3p-probe requires bulk IN endpoint — not found")
			h.done()
			h.dev.Close()
			os.Exit(1)
		}

		// Pre-submit a long-running IN read goroutine BEFORE any OUT writes.
		type inResult struct {
			n   int
			err error
			buf []byte
		}
		presub := make(chan inResult, 1)
		go func() {
			buf := make([]byte, 256)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			n, err := h.inEP.ReadContext(ctx, buf)
			presub <- inResult{n, err, buf[:n]}
		}()
		fmt.Println("Pre-submitted IN read goroutine (20s timeout).")

		// Build nv3p v3 GetPlatformInfo CMD packet manually.
		// 28 bytes total: header(16) + size(4) + cmd(4) + checksum(4)
		hdrBytes := make([]byte, 16)
		binary.LittleEndian.PutUint32(hdrBytes[0:], 3) // version=3
		binary.LittleEndian.PutUint32(hdrBytes[4:], 1) // type=CMD
		binary.LittleEndian.PutUint32(hdrBytes[8:], 0) // seq=0
		binary.LittleEndian.PutUint32(hdrBytes[12:], 0) // reserved=0
		sizeBytes := make([]byte, 4)  // args_len=0
		cmdBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(cmdBytes, 1) // CmdGetPlatformInfo=1
		// v3 checksum covers header + cmd (not size field)
		var ck uint32
		for _, b := range hdrBytes { ck += uint32(b) }
		for _, b := range cmdBytes  { ck += uint32(b) }
		csumBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(csumBytes, ^ck+1)
		fmt.Printf("CMD bytes: hdr=%s size=%s cmd=%s csum=%s\n",
			hex.EncodeToString(hdrBytes), hex.EncodeToString(sizeBytes),
			hex.EncodeToString(cmdBytes), hex.EncodeToString(csumBytes))

		pollIN := func(label string) {
			buf := make([]byte, 256)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			n, err := h.inEP.ReadContext(ctx, buf)
			if n > 0 || err == nil {
				fmt.Printf("  [%s] IN: %d bytes  data=%s  err=%v\n", label, n, hex.EncodeToString(buf[:n]), err)
			} else {
				fmt.Printf("  [%s] IN: 0 bytes (timeout)\n", label)
			}
		}

		writes := []struct {
			label string
			data  []byte
		}{
			{"header(16B)", hdrBytes},
			{"size(4B)", sizeBytes},
			{"cmd(4B)", cmdBytes},
			{"checksum(4B)", csumBytes},
		}
		outCtx30 := context.Background()
		for _, w := range writes {
			fmt.Printf("OUT write %s...", w.label)
			ctx, cancel := context.WithTimeout(outCtx30, 5*time.Second)
			_, err := h.outEP.WriteContext(ctx, w.data)
			cancel()
			if err != nil {
				fmt.Printf(" FAILED: %v\n", err)
				break
			}
			fmt.Println(" OK")
			pollIN("after-" + w.label)
		}

		// Also try entire CMD as a single write
		allCmd := append(append(append(hdrBytes, sizeBytes...), cmdBytes...), csumBytes...)
		fmt.Printf("\nNow try sending full CMD as single 28B write...\n")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := h.outEP.WriteContext(ctx, allCmd)
		cancel()
		if err != nil {
			fmt.Printf("  FAILED: %v\n", err)
		} else {
			fmt.Println("  OK")
			pollIN("after-single-cmd")
		}

		fmt.Println("\nWaiting for pre-submitted IN goroutine result...")
		res := <-presub
		fmt.Printf("Pre-submitted IN: %d bytes  err=%v  data=%s\n",
			res.n, res.err, hex.EncodeToString(res.buf))

		h.done()
		h.dev.Close()
		return
	}

	if useNv3p {
		if h.inEP == nil {
			fmt.Println("nv3p mode requires bulk IN endpoint — not found")
			h.done()
			h.dev.Close()
			os.Exit(1)
		}
		tr := &usbTransport{in: h.inEP, out: h.outEP, chunk: 2048}
		client, _ := nv3p.NewClientT264(tr)

		fmt.Println("\n=== nv3p v3: IsAppletT264 (GetPlatformInfo handshake) ===")
		isApplet, err := client.IsAppletT264()
		if err != nil {
			fmt.Printf("IsAppletT264 FAILED: %v\n", err)
			h.done()
			h.dev.Close()
			os.Exit(1)
		}
		fmt.Printf("IsAppletT264 OK: status==4(applet)=%v\n", isApplet)

		for i, f := range files {
			data := payloads[i]
			fmt.Printf("\n=== nv3p DownloadT264File %s: %d bytes ===\n", f.typeName, len(data))
			t0 := time.Now()
			if err := client.DownloadT264File(f.typeName, data); err != nil {
				fmt.Printf("  FAILED in %.1fs: %v\n", time.Since(t0).Seconds(), err)
				h.done()
				h.dev.Close()
				os.Exit(1)
			}
			fmt.Printf("  OK in %.1fs\n", time.Since(t0).Seconds())
		}

		h.done()
		h.dev.Close()
		fmt.Println("\n=== Phase 1 nv3p complete — waiting for MB1 applet re-enumeration (up to 15s) ===")
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			devs, _ := usbCtx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
				return desc.Vendor == gousb.ID(vendorNVIDIA)
			})
			if len(devs) > 0 {
				for _, d := range devs {
					fmt.Printf("  Found NVIDIA pid=0x%04x addr=%d\n", uint16(d.Desc.Product), d.Desc.Address)
					d.Close()
				}
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Println("No NVIDIA device found after 15s")
		return
	}

	for i, f := range files {
		data := payloads[i]
		fmt.Printf("\n=== Sending %s: %d bytes  magic=%s ===\n",
			f.typeName, len(data), hex.EncodeToString(data[:4]))
		if verbose {
			fmt.Printf("  First 64 bytes:\n%s", hex.Dump(data[:min(64, len(data))]))
		}

		outCtx, outCancel := context.WithTimeout(context.Background(), time.Duration(outTimeoutSec)*time.Second)

		if concurrent && h.inEP != nil {
			// Submit IN read and OUT write simultaneously.
			// Tests whether reading IN data unblocks the OUT endpoint.
			fmt.Printf("  [concurrent] submitting IN read + OUT write simultaneously...\n")
			var wg sync.WaitGroup
			var inN int
			var inErr error
			var inData [512]byte

			inCancelCtx, inCancel := context.WithTimeout(context.Background(), time.Duration(outTimeoutSec)*time.Second)
			wg.Add(1)
			go func() {
				defer wg.Done()
				inN, inErr = h.inEP.ReadContext(inCancelCtx, inData[:])
				inCancel()
			}()

			t0 := time.Now()
			n, err := h.outEP.WriteContext(outCtx, data)
			outCancel()

			// Wait briefly for IN goroutine
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				inCancel()
				wg.Wait()
			}

			fmt.Printf("  OUT: %d bytes in %.1fs err=%v\n", n, time.Since(t0).Seconds(), err)
			fmt.Printf("  IN concurrent: %d bytes err=%v  data=%s\n", inN, inErr, hex.EncodeToString(inData[:inN]))

			if err != nil {
				h.done()
				h.dev.Close()
				os.Exit(1)
			}
		} else {
			// Send file in 2048-byte chunks (T264 bootROM transfer size limit).
			t0 := time.Now()
			total := 0
			const maxWrite = 2048
			failed := false
			for off := 0; off < len(data); {
				end := off + maxWrite
				if end > len(data) { end = len(data) }
				wCtx, wCancel := context.WithTimeout(context.Background(), time.Duration(outTimeoutSec)*time.Second)
				n, err := h.outEP.WriteContext(wCtx, data[off:end])
				wCancel()
				total += n
				if err != nil {
					elapsed := time.Since(t0)
					fmt.Printf("  Bulk OUT %s FAILED after %d bytes in %.1fs at offset %d: %v\n",
						f.typeName, total, elapsed.Seconds(), off, err)
					h.done()
					h.dev.Close()
					os.Exit(1)
				}
				off = end
				_ = failed
			}
			outCancel()
			elapsed := time.Since(t0)
			fmt.Printf("  %s: OK (%d bytes sent in %.1fs)\n", f.typeName, total, elapsed.Seconds())
		}

		// tegrarcm does NOT read IN after Phase 1 files — the device sends its
		// BL version string only after the bootROM session closes, not during it.
		// Reading IN here would consume the version string early and desync Phase 2a.
	}

	h.done()
	h.dev.Close()

	fmt.Println("\n=== Phase 1 complete — waiting for MB1 applet ===")
	// MB1 appears at pid 0x7026 immediately after Phase 1; it may re-enumerate once
	// (addr changes) as it finishes its own init sequence.
	fmt.Print("Watching for MB1:")
	var mb1Dev *gousb.Device
	p2Limit := time.Now().Add(40 * time.Second)
	for time.Now().Before(p2Limit) {
		d, _ := usbCtx.OpenDeviceWithVIDPID(vendorNVIDIA, pidThor)
		if d != nil {
			fmt.Printf(" addr=%d\n", d.Desc.Address)
			mb1Dev = d
			break
		}
		fmt.Print(".")
		time.Sleep(200 * time.Millisecond)
	}
	if mb1Dev == nil {
		fmt.Println("\nNo MB1 device found within 40s — aborting")
		return
	}

	// tegrarcm uses 16 KB chunks (USBDEVFS_BULK loop limit 0x4000 bytes).
	// Overridden by -chunk flag for diagnostic testing.
	p2Chunk := 16384
	if p2ChunkOverride > 0 {
		p2Chunk = p2ChunkOverride
	}

	// Phase 2a: BL version read (tegrarcm state=5).
	// MB1 sends its 68-byte version string over bulk IN after the bootROM session ends.
	// Since we stopped reading IN during Phase 1, the string is buffered in the device
	// and arrives immediately when we issue the IN token here.
	// tegrarcm opens a separate USB session, reads it, then closes before Phase 2 files.
	fmt.Println("\n=== Phase 2a: BL version read (68 bytes) ===")
	fmt.Printf("  device pid=0x%04x addr=%d\n", uint16(mb1Dev.Desc.Product), mb1Dev.Desc.Address)

	p2aIface, p2aDone, err := mb1Dev.DefaultInterface()
	if err != nil {
		fmt.Printf("  DefaultInterface: %v\n", err)
		mb1Dev.Close()
		return
	}
	var p2aOut *gousb.OutEndpoint
	var p2aIn *gousb.InEndpoint
	for _, ep := range p2aIface.Setting.Endpoints {
		if ep.TransferType != gousb.TransferTypeBulk {
			continue
		}
		if ep.Direction == gousb.EndpointDirectionIn && p2aIn == nil {
			p2aIn, _ = p2aIface.InEndpoint(int(ep.Number))
		} else if ep.Direction == gousb.EndpointDirectionOut && p2aOut == nil {
			p2aOut, _ = p2aIface.OutEndpoint(int(ep.Number))
		}
	}
	if p2aIn != nil {
		fmt.Printf("  IN ep%d maxPkt=%d", p2aIn.Desc.Number, p2aIn.Desc.MaxPacketSize)
	}
	if p2aOut != nil {
		fmt.Printf("  OUT ep%d maxPkt=%d", p2aOut.Desc.Number, p2aOut.Desc.MaxPacketSize)
	}
	fmt.Println()

	// -p2-prewrite: submit bct_mem[0:16384] as OUT goroutine BEFORE reading the IN
	// version string. The OUT pipe is in Open state now (just after SetAlternateInterface).
	// After the IN read completes, IOKit may put the OUT pipe in Paused state — but a
	// transfer already submitted to XHCI hardware will complete regardless of IOKit state.
	type prewriteResult struct{ n int; err error }
	var p2PrewriteCh chan prewriteResult
	p2BctMemOffset := 0
	if p2Prewrite && p2aOut != nil {
		p2PrewriteCh = make(chan prewriteResult, 1)
		data0 := phase2Payloads[0]
		end0 := p2Chunk
		if end0 > len(data0) {
			end0 = len(data0)
		}
		fmt.Printf("  [prewrite] submitting bct_mem[0:%d] OUT goroutine before IN read...\n", end0)
		go func(ep *gousb.OutEndpoint, chunk []byte) {
			wCtx, wCancel := context.WithTimeout(context.Background(), 30*time.Second)
			n, err := ep.WriteContext(wCtx, chunk)
			wCancel()
			p2PrewriteCh <- prewriteResult{n, err}
		}(p2aOut, data0[:end0])
	}

	if p2SkipVersionRead {
		fmt.Println("  skipping version read (-p2-skip-version-read)")
	} else if p2aIn == nil {
		fmt.Println("  no bulk IN endpoint — skipping version read")
	} else {
		vBuf := make([]byte, 68)
		// Short timeout: if buffered it arrives in <50ms; if not available within
		// 2s, the device hasn't sent it yet and we proceed anyway.
		vCtx, vCancel := context.WithTimeout(context.Background(), 2*time.Second)
		vn, verr := p2aIn.ReadContext(vCtx, vBuf)
		vCancel()
		if verr != nil {
			fmt.Printf("  BL version: %v (continuing)\n", verr)
		} else {
			fmt.Printf("  BL version (%d bytes): %q\n", vn, vBuf[:vn])
		}
	}

	if p2SleepAfterInMs > 0 {
		fmt.Printf("  sleeping %dms after IN read (device state transition time)...\n", p2SleepAfterInMs)
		time.Sleep(time.Duration(p2SleepAfterInMs) * time.Millisecond)
	}

	// Collect prewrite goroutine result (if started).
	if p2PrewriteCh != nil {
		res := <-p2PrewriteCh
		if res.err != nil {
			fmt.Printf("  [prewrite] bct_mem[0:%d] FAILED: %v\n", p2Chunk, res.err)
			// Don't abort — fall through and let normal Phase 2b retry or fail naturally.
		} else {
			fmt.Printf("  [prewrite] bct_mem[0:%d] OK (%d bytes)\n", p2Chunk, res.n)
			p2BctMemOffset = p2Chunk
		}
	}

	// Phase 2b: raw bulk download — bct_mem then blob in a single USB session.
	// tegrarcm state=6-8: NvTegraUsbOpen → NvTegraUsbWriteTimeout(bct_mem) →
	//   NvTegraUsbWriteTimeout(blob) → NvTegraUsbClose.
	// No nv3p framing; no reconnect between the two files.
	//
	// -p2-same-session: reuse the Phase 2a device handle rather than close/reopen.
	// -p2-delay N: ms to sleep between Phase 2a close and Phase 2b open (default 200).
	var p2Out *gousb.OutEndpoint
	var p2In *gousb.InEndpoint
	var p2Done func()
	var mb1Dev2 *gousb.Device

	if p2SameSession {
		// Reuse Phase 2a session — no close/reopen.
		fmt.Println("\n=== Phase 2b: same-session — reusing Phase 2a interface ===")
		if p2aOut == nil {
			fmt.Println("  no OUT endpoint in Phase 2a session — cannot proceed")
			p2aDone()
			mb1Dev.Close()
			return
		}
		p2Out = p2aOut
		p2In = p2aIn
		p2Done = p2aDone
		mb1Dev2 = mb1Dev
		fmt.Printf("  OUT ep%d  maxPkt=%d", p2Out.Desc.Number, p2Out.Desc.MaxPacketSize)
		if p2In != nil {
			fmt.Printf("  IN ep%d maxPkt=%d", p2In.Desc.Number, p2In.Desc.MaxPacketSize)
		}
		fmt.Println()
	} else {
		// Standard path: close Phase 2a, watch for re-enumeration, open fresh session.
		p2aAddr := mb1Dev.Desc.Address
		p2aDone()
		mb1Dev.Close()
		fmt.Printf("\n=== Phase 2b: bct_mem + blob raw bulk (single USB session) ===\n")

		// Watch for device disappear/reappear after Phase 2a close — same pattern as
		// after-UID and post-Phase-1 waits. MB1 applet may trigger a USB re-enumeration
		// after sending the 68-byte version string, which would explain why a stale
		// handle (kIOReturnBusy) still fails even after pipe resets.
		fmt.Print("Watching for Phase 2b device:")
		disappeared := false
		p2bFound := false
		p2bDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(p2bDeadline) {
			time.Sleep(50 * time.Millisecond)
			d, _ := usbCtx.OpenDeviceWithVIDPID(vendorNVIDIA, pidThor)
			if d == nil {
				if !disappeared {
					fmt.Print(" [disappeared]")
					disappeared = true
				}
			} else {
				newAddr := d.Desc.Address
				d.Close()
				if disappeared {
					fmt.Printf(" [reappeared addr=%d was %d]", newAddr, p2aAddr)
				} else {
					fmt.Printf(" [stable addr=%d]", newAddr)
				}
				fmt.Println()
				p2bFound = true
				break
			}
		}
		if !p2bFound {
			if disappeared {
				fmt.Println("\nDevice did not reappear within 5s — aborting Phase 2b")
			} else {
				fmt.Println("\nDevice not found within 5s — aborting Phase 2b")
			}
			return
		}
		if p2DelayMs > 0 {
			time.Sleep(time.Duration(p2DelayMs) * time.Millisecond)
		}

		var err2 error
		mb1Dev2, err2 = usbCtx.OpenDeviceWithVIDPID(vendorNVIDIA, pidThor)
		if err2 != nil || mb1Dev2 == nil {
			fmt.Printf("Device not found for Phase 2b: %v\n", err2)
			return
		}
		fmt.Printf("  device pid=0x%04x addr=%d\n", uint16(mb1Dev2.Desc.Product), mb1Dev2.Desc.Address)

		var p2Iface *gousb.Interface
		p2Iface, p2Done, err2 = mb1Dev2.DefaultInterface()
		if err2 != nil {
			fmt.Printf("  DefaultInterface: %v\n", err2)
			mb1Dev2.Close()
			return
		}
		for _, ep := range p2Iface.Setting.Endpoints {
			if ep.TransferType != gousb.TransferTypeBulk {
				continue
			}
			if ep.Direction == gousb.EndpointDirectionOut && p2Out == nil {
				p2Out, _ = p2Iface.OutEndpoint(int(ep.Number))
			} else if ep.Direction == gousb.EndpointDirectionIn && p2In == nil {
				p2In, _ = p2Iface.InEndpoint(int(ep.Number))
			}
		}
		if p2Out == nil {
			fmt.Println("  no bulk OUT endpoint for Phase 2b")
			p2Done()
			mb1Dev2.Close()
			return
		}
		fmt.Printf("  OUT ep%d  maxPkt=%d", p2Out.Desc.Number, p2Out.Desc.MaxPacketSize)
		if p2In != nil {
			fmt.Printf("  IN ep%d maxPkt=%d", p2In.Desc.Number, p2In.Desc.MaxPacketSize)
		}
		fmt.Println()

		// On Darwin, DefaultInterface() always calls SetAlternateInterface(0) which sends
		// SET_INTERFACE(0,0) to the device. MB1 applet treats this as a new session and
		// re-queues its 68-byte version string. Read it here to drain before OUT writes.
		if p2RereadVersion && p2In != nil {
			vBuf := make([]byte, 68)
			vCtx, vCancel := context.WithTimeout(context.Background(), 2*time.Second)
			vn, verr := p2In.ReadContext(vCtx, vBuf)
			vCancel()
			if verr != nil {
				fmt.Printf("  Phase 2b version re-read: %v (continuing)\n", verr)
			} else {
				fmt.Printf("  Phase 2b version re-read (%d bytes): %q\n", vn, vBuf[:vn])
			}
		}
	}

	// Pre-write diagnostics: check endpoint halt status, drain IN, etc.
	if p2GetStatus {
		// GET_STATUS for OUT endpoint (addr=p2Out.Desc.Number) and IN endpoint.
		outAddr := uint16(p2Out.Desc.Number)
		statBuf := make([]byte, 2)
		n, err := mb1Dev2.Control(0x82, 0x00, 0x0000, outAddr, statBuf)
		if err != nil {
			fmt.Printf("  GET_STATUS(OUT EP%d): error %v\n", outAddr, err)
		} else {
			status := binary.LittleEndian.Uint16(statBuf[:n])
			fmt.Printf("  GET_STATUS(OUT EP%d): 0x%04x  HALT=%v\n", outAddr, status, status&1 != 0)
		}
		if p2In != nil {
			inAddr := uint16(p2In.Desc.Number) | 0x80
			n2, err2 := mb1Dev2.Control(0x82, 0x00, 0x0000, inAddr, statBuf)
			if err2 != nil {
				fmt.Printf("  GET_STATUS(IN EP%d): error %v\n", inAddr, err2)
			} else {
				status2 := binary.LittleEndian.Uint16(statBuf[:n2])
				fmt.Printf("  GET_STATUS(IN EP%d): 0x%04x  HALT=%v\n", inAddr, status2, status2&1 != 0)
			}
		}
	}
	if p2ClearHalt {
		// CLEAR_FEATURE(ENDPOINT_HALT) to OUT endpoint — unstalls on both host and device.
		outAddr := uint16(p2Out.Desc.Number)
		_, err := mb1Dev2.Control(0x02, 0x01, 0x0000, outAddr, nil)
		if err != nil {
			fmt.Printf("  CLEAR_FEATURE(OUT EP%d): FAILED: %v\n", outAddr, err)
		} else {
			fmt.Printf("  CLEAR_FEATURE(OUT EP%d): OK\n", outAddr)
		}
	}
	if p2SetInterface {
		// SET_INTERFACE(interface=0, alt=0) — resets endpoint DATA toggles on device.
		_, err := mb1Dev2.Control(0x01, 0x0B, 0x0000, 0x0000, nil)
		if err != nil {
			fmt.Printf("  SET_INTERFACE(0,0): FAILED: %v\n", err)
		} else {
			fmt.Printf("  SET_INTERFACE(0,0): OK\n")
		}
	}
	if p2LibusbClearHalt {
		// libusb_clear_halt calls IOKit ClearPipeStall which:
		//   1. Sends CLEAR_FEATURE(HALT) control transfer to the device
		//   2. Resets the HOST-side IOKit pipe state to kIOUSBHostPipeStateOpen
		// This fixes kIOReturnBusy (0xe00002ed) errors where the OUT pipe is left
		// non-open after an IN read on the same interface.
		outAddr := uint8(p2Out.Desc.Number) // 0x01 = OUT pipe
		if err := libusb_clear_halt(mb1Dev2, outAddr); err != nil {
			fmt.Printf("  libusb_clear_halt(OUT 0x%02x): FAILED: %v\n", outAddr, err)
		} else {
			fmt.Printf("  libusb_clear_halt(OUT 0x%02x): OK\n", outAddr)
		}
	}
	if p2LibusbSetAlt {
		// libusb_set_interface_alt_setting calls IOKit SetAlternateInterface which resets
		// ALL endpoint DATA toggles and pipe states on both host and device sides.
		if err := libusb_set_interface_alt(mb1Dev2, 0, 0); err != nil {
			fmt.Printf("  libusb_set_interface_alt(0,0): FAILED: %v\n", err)
		} else {
			fmt.Printf("  libusb_set_interface_alt(0,0): OK\n")
		}
	}
	if p2DrainIn && p2In != nil {
		// Drain any extra IN data MB1 may have queued after the 68-byte version string.
		drainBuf := make([]byte, 512)
		dCtx, dCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		dn, derr := p2In.ReadContext(dCtx, drainBuf)
		dCancel()
		if dn > 0 {
			fmt.Printf("  drain-in: consumed extra %d bytes: %s\n", dn, hex.EncodeToString(drainBuf[:dn]))
		} else {
			fmt.Printf("  drain-in: nothing extra (err=%v)\n", derr)
		}
	}
	if p2AbortIn && p2In != nil {
		// ClearPipeStallBothEnds on the IN endpoint to cancel any IOKit auto-poll.
		// Hypothesis: IOKit submits a persistent internal IN read when the device has
		// pending IN data; this blocks OUT ep1 with kIOReturnBusy. Aborting IN clears
		// this internal poll and should allow OUT writes to proceed.
		inAddr := uint8(p2In.Desc.Number) | 0x80 // ep1 IN = 0x81
		if err := libusb_clear_halt(mb1Dev2, inAddr); err != nil {
			fmt.Printf("  libusb_clear_halt(IN 0x%02x): FAILED: %v\n", inAddr, err)
		} else {
			fmt.Printf("  libusb_clear_halt(IN 0x%02x): OK — IOKit auto-poll should be cleared\n", inAddr)
		}
	}

	if p2Nv3p {
		fmt.Println("\n=== Phase 2b: nv3p v3 (IsAppletT264 + DownloadT264File) ===")
		// Use asyncUSBTransport so an IN URB is always pending before the device
		// sends its RESPONSE_CMD. Without pre-submission, IOKit stops issuing IN
		// tokens between the last OUT write and the nv3p read call, and MB1's
		// response can be missed.
		atr := newAsyncUSBTransport(p2In, p2Out, p2Chunk, time.Duration(p2ReadTimeoutSec)*time.Second, true)
		// atr.Close() MUST be called before p2Done()/mb1Dev2.Close() to cancel
		// in-flight libusb transfers before the device handle is freed (prevents SIGSEGV).
		p2NvCleanup := func() {
			atr.Close()
			p2Done()
			mb1Dev2.Close()
		}
		client, _ := nv3p.NewClientT264(atr)

		if !p2Nv3pNoIsApplet {
			fmt.Println("  IsAppletT264 (GetPlatformInfo)...")
			t0 := time.Now()
			isApplet, err := client.IsAppletT264()
			if err != nil {
				fmt.Printf("  IsAppletT264 FAILED in %.1fs: %v\n", time.Since(t0).Seconds(), err)
				p2NvCleanup()
				return
			}
			fmt.Printf("  IsAppletT264 OK in %.1fs: applet=%v\n", time.Since(t0).Seconds(), isApplet)
		} else {
			fmt.Println("  (skipping IsAppletT264)")
		}

		for i, f := range phase2Files {
			data := phase2Payloads[i]
			fmt.Printf("\n  DownloadT264File %q: %d bytes...\n", f.typeName, len(data))
			t0 := time.Now()
			if err := client.DownloadT264File(f.typeName, data); err != nil {
				fmt.Printf("  FAILED in %.1fs: %v\n", time.Since(t0).Seconds(), err)
				p2NvCleanup()
				return
			}
			fmt.Printf("  OK in %.1fs\n", time.Since(t0).Seconds())
		}

		p2NvCleanup()
		fmt.Println("\n=== Phase 2 nv3p complete ===")
		return
	}

	// rawAtr keeps an IN URB always pending during raw bulk writes (diagnostic).
	// Hypothesis: kIOReturnNotResponding after IN read is caused by IOKit leaving the
	// OUT pipe non-open; having a concurrent pending IN URB prevents this.
	var rawAtr *asyncUSBTransport
	if p2AsyncIn && p2In != nil {
		rawAtr = newAsyncUSBTransport(p2In, p2Out, 0, time.Duration(p2ReadTimeoutSec)*time.Second, true)
	}
	rawCloseAtr := func() {
		if rawAtr != nil {
			rawAtr.Close()
			rawAtr = nil
		}
	}

	for p2Reconnects := 0; ; {
		gotNoDevice := false
		for i, f := range phase2Files {
			data := phase2Payloads[i]
			fmt.Printf("\n  Sending %s (%d bytes, %d-byte chunks)...\n", f.typeName, len(data), p2Chunk)
			t0 := time.Now()
			startOff := 0
			if i == 0 && p2BctMemOffset > 0 {
				startOff = p2BctMemOffset
				fmt.Printf("  (prewrite covered first %d bytes, starting at offset %d)\n", startOff, startOff)
			}
			total := startOff // count prewritten bytes as sent
			for off := startOff; off < len(data); {
				end := off + p2Chunk
				if end > len(data) {
					end = len(data)
				}
				var n int
				var werr error
				for attempt := 0; attempt <= p2RetryCount; attempt++ {
					if attempt > 0 {
						// "no device" means the device is gone — skip retries and reconnect.
						if strings.Contains(werr.Error(), "no device") {
							break
						}
						fmt.Printf("    [retry %d/%d after 100ms: %v]\n", attempt, p2RetryCount, werr)
						time.Sleep(100 * time.Millisecond)
					}
					if p2CgoWrite {
						n, werr = libusb_bulk_write(mb1Dev2, uint8(p2Out.Desc.Number), data[off:end], 5000)
					} else {
						wCtx, wCancel := context.WithTimeout(context.Background(), 30*time.Second)
						n, werr = p2Out.WriteContext(wCtx, data[off:end])
						wCancel()
					}
					if werr == nil {
						break
					}
				}
				total += n
				if werr != nil {
					if strings.Contains(werr.Error(), "no device") && p2Reconnects < p2ReconnectCount {
						fmt.Printf("  %s: device lost at offset %d — reconnect %d/%d\n",
							f.typeName, off, p2Reconnects+1, p2ReconnectCount)
						gotNoDevice = true
					} else {
						fmt.Printf("  %s FAILED at offset %d (%d bytes written) after %d attempts: %v\n",
							f.typeName, off, total, p2RetryCount+1, werr)
						rawCloseAtr()
						p2Done()
						mb1Dev2.Close()
						return
					}
					break
				}
				off = end

				// Optionally do a blocking IN read after each chunk write.
				// Tests whether MB1 sends a per-chunk ACK that must be consumed
				// before the next OUT write will succeed.
				if p2ReadAfterChunk && p2In != nil && rawAtr == nil {
					rBuf := make([]byte, 512)
					rCtx, rCancel := context.WithTimeout(context.Background(), time.Duration(p2AfterChunkMs)*time.Millisecond)
					rn, rerr := p2In.ReadContext(rCtx, rBuf)
					rCancel()
					if rn > 0 {
						fmt.Printf("    [mid-read @%d] got %dB: %s\n", off, rn, hex.EncodeToString(rBuf[:rn]))
					} else {
						fmt.Printf("    [mid-read @%d] no data (%v)\n", off, rerr)
					}
				}
			}
			if gotNoDevice {
				break
			}
			fmt.Printf("  %s: OK (%d bytes in %.1fs)\n", f.typeName, total, time.Since(t0).Seconds())

			// Read any response from device after each file.
			// Skip if async IN is active — the goroutine already logs all IN data.
			if p2In != nil && rawAtr == nil {
				rBuf := make([]byte, 256)
				rCtx, rCancel := context.WithTimeout(context.Background(), 2*time.Second)
				rn, _ := p2In.ReadContext(rCtx, rBuf)
				rCancel()
				if rn > 0 {
					fmt.Printf("  response after %s: %s\n", f.typeName, hex.EncodeToString(rBuf[:rn]))
				} else {
					fmt.Printf("  response after %s: none (2s timeout)\n", f.typeName)
				}
			}
		}

		if !gotNoDevice {
			break
		}

		// Reconnect: close stale handle, wait for re-enum, reopen.
		p2Reconnects++
		rawCloseAtr() // must precede p2Done/mb1Dev2.Close
		p2Done()
		mb1Dev2.Close()
		p2Out = nil
		p2In = nil

		fmt.Print("  Waiting for device after reset:")
		p2reFound := false
		p2reDeadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(p2reDeadline) {
			time.Sleep(100 * time.Millisecond)
			d, _ := usbCtx.OpenDeviceWithVIDPID(vendorNVIDIA, pidThor)
			if d != nil {
				fmt.Printf(" addr=%d\n", d.Desc.Address)
				d.Close()
				p2reFound = true
				break
			}
			fmt.Print(".")
		}
		if !p2reFound {
			fmt.Println("\n  Device did not reappear within 30s — aborting")
			return
		}
		time.Sleep(200 * time.Millisecond)

		var reErr error
		mb1Dev2, reErr = usbCtx.OpenDeviceWithVIDPID(vendorNVIDIA, pidThor)
		if reErr != nil || mb1Dev2 == nil {
			fmt.Printf("  Reopen failed: %v\n", reErr)
			return
		}
		fmt.Printf("  Reopened: pid=0x%04x addr=%d\n", uint16(mb1Dev2.Desc.Product), mb1Dev2.Desc.Address)

		var reIface *gousb.Interface
		reIface, p2Done, reErr = mb1Dev2.DefaultInterface()
		if reErr != nil {
			fmt.Printf("  DefaultInterface: %v\n", reErr)
			mb1Dev2.Close()
			return
		}
		for _, ep := range reIface.Setting.Endpoints {
			if ep.TransferType != gousb.TransferTypeBulk {
				continue
			}
			if ep.Direction == gousb.EndpointDirectionOut && p2Out == nil {
				p2Out, _ = reIface.OutEndpoint(int(ep.Number))
			} else if ep.Direction == gousb.EndpointDirectionIn && p2In == nil {
				p2In, _ = reIface.InEndpoint(int(ep.Number))
			}
		}
		if p2Out == nil {
			fmt.Println("  No bulk OUT on reconnected device — aborting")
			p2Done()
			mb1Dev2.Close()
			return
		}
		fmt.Printf("  OUT ep%d  maxPkt=%d", p2Out.Desc.Number, p2Out.Desc.MaxPacketSize)
		if p2In != nil {
			fmt.Printf("  IN ep%d maxPkt=%d", p2In.Desc.Number, p2In.Desc.MaxPacketSize)
		}
		fmt.Println()

		// Re-read version string from the reconnected MB1 applet (short timeout).
		if p2In != nil {
			vBuf := make([]byte, 68)
			vCtx, vCancel := context.WithTimeout(context.Background(), 2*time.Second)
			vn, verr := p2In.ReadContext(vCtx, vBuf)
			vCancel()
			if verr != nil {
				fmt.Printf("  version read after reconnect: %v\n", verr)
			} else {
				fmt.Printf("  version read after reconnect (%d bytes): %q\n", vn, vBuf[:vn])
			}
		}
		fmt.Println("  Retrying Phase 2b writes from offset 0...")
	}

	rawCloseAtr()
	p2Done()
	mb1Dev2.Close()
	fmt.Println("\n=== Phase 2 complete ===")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// usbTransport adapts gousb bulk endpoints to the nv3p transport interface.
// chunk limits the maximum bytes per USB write (0 = unlimited).
// readTimeout overrides the default 10s per-read timeout (0 = use 10s).
type usbTransport struct {
	in          *gousb.InEndpoint
	out         *gousb.OutEndpoint
	chunk       int
	readTimeout time.Duration
}

func (t *usbTransport) Read(buf []byte) (int, error) {
	timeout := t.readTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return t.in.ReadContext(ctx, buf)
}

func (t *usbTransport) Write(buf []byte) error {
	chunk := t.chunk
	if chunk == 0 {
		chunk = len(buf)
	}
	for len(buf) > 0 {
		n := len(buf)
		if n > chunk {
			n = chunk
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := t.out.WriteContext(ctx, buf[:n])
		cancel()
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}

// asyncUSBTransport wraps USB bulk endpoints for the nv3p transport interface.
// Unlike usbTransport (which submits one IN URB per Read call), this type keeps
// a background goroutine continuously re-submitting IN reads so that data sent
// by the device between OUT writes is buffered immediately, not dropped due to
// IOKit stopping IN tokens when no host request is queued.
//
// IMPORTANT: always call Close() before closing the gousb Device. Close() cancels
// the goroutine's in-flight ReadContext and waits for the goroutine to exit; this
// ensures libusb_cancel_transfer is never called on a freed transfer handle.
type asyncUSBTransport struct {
	out         *gousb.OutEndpoint
	chunk       int
	readTimeout time.Duration
	verbose     bool // log all received IN data to stderr

	rxCh   chan []byte
	rxBuf  []byte
	cancel context.CancelFunc
	done   chan struct{}
}

func newAsyncUSBTransport(in *gousb.InEndpoint, out *gousb.OutEndpoint, chunk int, readTimeout time.Duration, verbose bool) *asyncUSBTransport {
	ctx, cancel := context.WithCancel(context.Background())
	t := &asyncUSBTransport{
		out:         out,
		chunk:       chunk,
		readTimeout: readTimeout,
		verbose:     verbose,
		rxCh:        make(chan []byte, 64),
		cancel:      cancel,
		done:        make(chan struct{}),
	}
	go func() {
		defer close(t.done)
		defer close(t.rxCh)
		for {
			buf := make([]byte, 4096)
			// Use ctx as parent so Close() cancels any in-flight ReadContext immediately.
			readCtx, readCancel := context.WithTimeout(ctx, readTimeout)
			n, err := in.ReadContext(readCtx, buf)
			readCancel()
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				if t.verbose {
					fmt.Fprintf(os.Stderr, "[asyncIN] +%dB: %s\n", n, hex.EncodeToString(data))
				}
				select {
				case t.rxCh <- data:
				case <-ctx.Done():
					return
				}
			}
			if ctx.Err() != nil {
				return // context cancelled by Close() — exit cleanly
			}
			if err != nil {
				s := err.Error()
				if strings.Contains(s, "cancelled") || strings.Contains(s, "deadline") || strings.Contains(s, "timeout") {
					continue // re-submit after timeout — keep polling
				}
				if t.verbose {
					fmt.Fprintf(os.Stderr, "[asyncIN] goroutine exiting: %v\n", err)
				}
				return // real error (no device, pipe error, etc.)
			}
		}
	}()
	return t
}

func (t *asyncUSBTransport) Read(buf []byte) (int, error) {
	for len(t.rxBuf) == 0 {
		select {
		case data, ok := <-t.rxCh:
			if !ok {
				return 0, fmt.Errorf("USB IN stream ended")
			}
			t.rxBuf = data
		case <-time.After(t.readTimeout * 2):
			return 0, fmt.Errorf("nv3p IN read timeout after %v", t.readTimeout*2)
		}
	}
	n := copy(buf, t.rxBuf)
	t.rxBuf = t.rxBuf[n:]
	return n, nil
}

func (t *asyncUSBTransport) Write(buf []byte) error {
	chunk := t.chunk
	if chunk == 0 {
		chunk = len(buf)
	}
	for len(buf) > 0 {
		n := len(buf)
		if n > chunk {
			n = chunk
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := t.out.WriteContext(ctx, buf[:n])
		cancel()
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}

// Close cancels the background goroutine's in-flight ReadContext and waits for
// the goroutine to exit. Must be called before closing the gousb Device.
func (t *asyncUSBTransport) Close() {
	t.cancel()
	<-t.done
}
