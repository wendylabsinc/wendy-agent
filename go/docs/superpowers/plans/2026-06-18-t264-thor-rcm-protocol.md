# T264 Thor RCM Protocol Implementation Plan

> **SUPERSEDED IN PART (2026-06-24).** This plan was written before live-hardware validation.
> Two of its premises were wrong and have been corrected in code (branch
> `thombles/emmc-thor-flash`):
> - **No "RCM state" probe.** String descriptor 3 is the chip BR_CID, not a state machine.
>   `RCMState`/`parseStateDescriptor` → `ReadChipID`/`parseChipIDDescriptor`; the state gate
>   is gone. (Tasks 2–3 below describing a state probe are obsolete.)
> - **Images are sent verbatim**, not wrapped via `BuildDLMiniloader`. `LoadImagesT23x` →
>   `DownloadBootROMImages` (file `t23x.go` → `bootrom.go`); `Device.Write` now chunks to
>   16 KiB + sends a ZLP.
>
> See the design doc and protocol notes for the corrected
> protocol. The bootROM order is `bct_br → mb1 → psc_bl1 → bct_mb1`; BCTs are generated at
> flash time (replay via `cmd/thor-replay`).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make T264 (Thor, USB PID 0x7026) flash end-to-end by implementing the T23x multi-image RCM download sequence.

**Architecture:** Add `ParseRCMImages` to the bundle package (parses `rcmboot-flash.xml.in` from the tegraflash tarball), add `ControlRead`/`RCMState`/`IsT264` to the rcm Device (USB GET_STRING_DESCRIPTOR state probe), add `LoadImagesT23x` (sends mb1, psc_bl1, applet as sequential RCM40 bulk writes), then branch in flash.go on chip ID.

**Tech Stack:** Go 1.26.3, `github.com/google/gousb v1.1.3`, `encoding/xml`, `crypto/aes`.

## Global Constraints

- Module: `github.com/wendylabsinc/wendy`
- Build tag on all rcm package files (including tests): `//go:build darwin || linux`
- No new external dependencies.
- `rcm/message.go` is correct and must not change.
- `rcm/constants.go`, `nv3p/`, `flash.go` existing behaviour for T234 must be unchanged.
- Run `go build ./...` after every task to catch compile errors before committing.

---

### Task 1: RCM image list parsing

Parse the `device type="rcm"` block from `rcmboot-flash.xml.in` in the bundle, returning only entries that have a non-empty filename.

**Files:**
- Modify: `internal/cli/tegraflash/bundle/xml.go`
- Create: `internal/cli/tegraflash/bundle/xml_test.go`

**Interfaces:**
- Produces:
  - `type RCMImage struct { Name, Type, Filename string }` (exported, in package `bundle`)
  - `func ParseRCMImages(data []byte) ([]RCMImage, error)` — parses XML bytes, returns ordered slice
  - `func (b *Bundle) RCMImages() ([]RCMImage, error)` — extracts `rcmboot-flash.xml.in` from bundle and calls `ParseRCMImages`

---

- [ ] **Step 1: Write the failing test**

Create `internal/cli/tegraflash/bundle/xml_test.go`:

```go
package bundle

import (
	"strings"
	"testing"
)

const rcmbootXML = `<?xml version="1.0"?>
<partition_layout version="01.00.0000">
    <device type="rcm" instance="0" sector_size="512" num_sectors="262144">
        <partition name="mb1" type="mb1_bootloader">
            <filename> mb1_t264_prod.bin </filename>
        </partition>
        <partition name="psc_bl1" type="psc_bl1">
            <filename> psc_bl1_t264_prod.bin </filename>
        </partition>
        <partition name="mb2-applet" type="mb2_applet">
            <filename> applet_t264.bin </filename>
        </partition>
        <partition name="MEM_BCT" type="mem_boot_config_table">
            <filename> </filename>
        </partition>
        <partition name="MEM_DTB" type="mem_dtb">
            <filename></filename>
        </partition>
    </device>
    <device type="spi" instance="0" sector_size="512" num_sectors="131072">
        <partition name="mb1" type="mb1_bootloader">
            <filename> mb1_t264_prod.bin </filename>
        </partition>
    </device>
</partition_layout>`

func TestParseRCMImages(t *testing.T) {
	images, err := ParseRCMImages([]byte(rcmbootXML))
	if err != nil {
		t.Fatalf("ParseRCMImages() error = %v", err)
	}
	want := []RCMImage{
		{Name: "mb1", Type: "mb1_bootloader", Filename: "mb1_t264_prod.bin"},
		{Name: "psc_bl1", Type: "psc_bl1", Filename: "psc_bl1_t264_prod.bin"},
		{Name: "mb2-applet", Type: "mb2_applet", Filename: "applet_t264.bin"},
	}
	if len(images) != len(want) {
		t.Fatalf("len(images) = %d, want %d; got %+v", len(images), len(want), images)
	}
	for i, got := range images {
		if got != want[i] {
			t.Errorf("images[%d] = %+v, want %+v", i, got, want[i])
		}
	}
}

func TestParseRCMImages_NoRCMDevice(t *testing.T) {
	xml := `<?xml version="1.0"?>
<partition_layout version="01.00.0000">
    <device type="spi" instance="0" sector_size="512" num_sectors="131072">
        <partition name="mb1" type="mb1_bootloader">
            <filename> mb1_t264_prod.bin </filename>
        </partition>
    </device>
</partition_layout>`
	_, err := ParseRCMImages([]byte(xml))
	if err == nil {
		t.Fatal("ParseRCMImages() expected error for XML with no rcm device, got nil")
	}
	if !strings.Contains(err.Error(), "no rcm device") {
		t.Errorf("error = %q, want it to mention 'no rcm device'", err.Error())
	}
}

func TestParseRCMImages_AllEmpty(t *testing.T) {
	xml := `<?xml version="1.0"?>
<partition_layout version="01.00.0000">
    <device type="rcm" instance="0" sector_size="512" num_sectors="262144">
        <partition name="MEM_BCT" type="mem_boot_config_table">
            <filename> </filename>
        </partition>
    </device>
</partition_layout>`
	images, err := ParseRCMImages([]byte(xml))
	if err != nil {
		t.Fatalf("ParseRCMImages() unexpected error: %v", err)
	}
	if len(images) != 0 {
		t.Errorf("expected 0 images when all filenames empty, got %d", len(images))
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

```bash
cd /path/to/repo/go
go test ./internal/cli/tegraflash/bundle/ -run TestParseRCMImages -v
```

Expected: `FAIL — undefined: ParseRCMImages` (or `undefined: RCMImage`).

- [ ] **Step 3: Implement `RCMImage`, `ParseRCMImages`, and `Bundle.RCMImages`**

Add to the bottom of `internal/cli/tegraflash/bundle/xml.go`:

```go
// RCMImage is one entry from the device type="rcm" block of rcmboot-flash.xml.in.
type RCMImage struct {
	Name     string
	Type     string
	Filename string
}

// ParseRCMImages parses a tegraflash partition XML and returns the ordered list
// of partitions in the device type="rcm" block that have non-empty filenames.
// Partitions with empty or whitespace-only filenames (BCT placeholders etc.) are skipped.
func ParseRCMImages(data []byte) ([]RCMImage, error) {
	var layout PartitionLayout
	if err := xml.Unmarshal(data, &layout); err != nil {
		return nil, err
	}
	for _, dev := range layout.Devices {
		if dev.Type != "rcm" {
			continue
		}
		var images []RCMImage
		for _, p := range dev.Partitions {
			if !p.HasFile() {
				continue
			}
			images = append(images, RCMImage{
				Name:     p.Name,
				Type:     p.Type,
				Filename: strings.TrimSpace(p.Filename),
			})
		}
		return images, nil
	}
	return nil, fmt.Errorf("no rcm device block found in partition XML")
}

// RCMImages parses rcmboot-flash.xml.in from the bundle and returns the ordered
// list of RCM-phase images. Partitions without filenames are omitted.
func (b *Bundle) RCMImages() ([]RCMImage, error) {
	data, err := b.ExtractFile("rcmboot-flash.xml.in")
	if err != nil {
		return nil, fmt.Errorf("rcmboot-flash.xml.in not found in bundle: %w", err)
	}
	return ParseRCMImages(data)
}
```

Add `"fmt"` and `"strings"` to the import block in `xml.go` if not already present. The file currently imports only `"encoding/xml"`, `"strconv"`, and `"strings"`. Add `"fmt"`.

- [ ] **Step 4: Run the tests to confirm they pass**

```bash
go test ./internal/cli/tegraflash/bundle/ -run TestParseRCMImages -v
```

Expected:
```
--- PASS: TestParseRCMImages (0.00s)
--- PASS: TestParseRCMImages_NoRCMDevice (0.00s)
--- PASS: TestParseRCMImages_AllEmpty (0.00s)
PASS
```

- [ ] **Step 5: Verify the build**

```bash
go build ./...
```

Expected: no output (clean build).

- [ ] **Step 6: Commit**

```bash
git add internal/cli/tegraflash/bundle/xml.go internal/cli/tegraflash/bundle/xml_test.go
git commit -m "feat(bundle): add ParseRCMImages for T264 RCM image list"
```

---

### Task 2: USB state probe and chip detection

Add `ProductID`, `IsT264`, `ControlRead`, `RCMState`, and the unexported `parseStateDescriptor` helper to `rcm/device.go`.

**Files:**
- Modify: `internal/cli/tegraflash/rcm/device.go`
- Create: `internal/cli/tegraflash/rcm/device_test.go`

**Interfaces:**
- Consumes: `ProductThor` constant (already in `rcm/constants.go`)
- Produces:
  - `func (d *Device) ProductID() gousb.ID`
  - `func (d *Device) IsT264() bool`
  - `func (d *Device) ControlRead(buf []byte) (int, error)`
  - `func (d *Device) RCMState() (byte, error)`
  - `func parseStateDescriptor(buf []byte, n int) (byte, error)` (unexported)

---

- [ ] **Step 1: Write the failing test**

Create `internal/cli/tegraflash/rcm/device_test.go`:

```go
//go:build darwin || linux

package rcm

import "testing"

func TestParseStateDescriptor(t *testing.T) {
	tests := []struct {
		name    string
		buf     []byte
		n       int
		want    byte
		wantErr bool
	}{
		{
			name: "state 0 initial",
			buf:  []byte{0x06, 0x03, 0x00, 0x00, 0x00, 0x00},
			n:    6,
			want: 0,
		},
		{
			name: "state 5 MB2 applet running",
			buf:  []byte{0x06, 0x03, 0x05, 0x00, 0x00, 0x00},
			n:    6,
			want: 5,
		},
		{
			name: "state 8 MB2 running",
			buf:  []byte{0x06, 0x03, 0x08, 0x00, 0x00, 0x00},
			n:    6,
			want: 8,
		},
		{
			name:    "n=2 too short",
			buf:     []byte{0x04, 0x03},
			n:       2,
			wantErr: true,
		},
		{
			name:    "n=0 empty read",
			buf:     make([]byte, 96),
			n:       0,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStateDescriptor(tt.buf, tt.n)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseStateDescriptor() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseStateDescriptor() = %d, want %d", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

```bash
go test ./internal/cli/tegraflash/rcm/ -run TestParseStateDescriptor -v
```

Expected: `FAIL — undefined: parseStateDescriptor`.

- [ ] **Step 3: Implement the new methods**

Append to `internal/cli/tegraflash/rcm/device.go` (after the `LoadApplet` method):

```go
// ProductID returns the USB product ID of the connected device.
func (d *Device) ProductID() gousb.ID {
	return d.dev.Desc.Product
}

// IsT264 reports whether the device is a T264 (Thor) chip.
func (d *Device) IsT264() bool {
	return d.ProductID() == ProductThor
}

// ControlRead reads a USB string descriptor from the bootROM via endpoint 0.
// T23x bootROMs encode RCM state in string descriptor index 3 using a
// GET_DESCRIPTOR control transfer (bmRequestType=0x80, bRequest=0x06,
// wValue=0x0303, wIndex=0x0000). buf must be at least 3 bytes; 96 bytes is typical.
func (d *Device) ControlRead(buf []byte) (int, error) {
	return d.dev.Control(
		0x80,   // rType: IN, standard, device
		0x06,   // request: GET_DESCRIPTOR
		0x0303, // val: STRING descriptor type (0x03), index 3
		0x0000, // idx: language 0
		buf,
	)
}

// RCMState reads the T23x bootROM RCM state via USB control transfer.
// State 0 means the device is freshly reset and ready for image download.
func (d *Device) RCMState() (byte, error) {
	buf := make([]byte, 96)
	n, err := d.ControlRead(buf)
	if err != nil {
		return 0, fmt.Errorf("reading RCM state descriptor: %w", err)
	}
	return parseStateDescriptor(buf, n)
}

// parseStateDescriptor extracts the RCM state byte from a GET_STRING_DESCRIPTOR response.
// NVIDIA's T23x bootROM encodes the RCM state as the first byte of the UTF-16LE
// payload (buf[2]). Derived from RE of tegrarcm_v2 mainT23x (Thor nightly 20260618).
func parseStateDescriptor(buf []byte, n int) (byte, error) {
	if n < 3 {
		return 0, fmt.Errorf("RCM state descriptor too short: got %d bytes, need at least 3", n)
	}
	return buf[2], nil
}
```

- [ ] **Step 4: Run the test to confirm it passes**

```bash
go test ./internal/cli/tegraflash/rcm/ -run TestParseStateDescriptor -v
```

Expected:
```
--- PASS: TestParseStateDescriptor/state_0_initial (0.00s)
--- PASS: TestParseStateDescriptor/state_5_MB2_applet_running (0.00s)
--- PASS: TestParseStateDescriptor/state_8_MB2_running (0.00s)
--- PASS: TestParseStateDescriptor/n=2_too_short (0.00s)
--- PASS: TestParseStateDescriptor/n=0_empty_read (0.00s)
PASS
```

- [ ] **Step 5: Verify the build**

```bash
go build ./...
```

Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/tegraflash/rcm/device.go internal/cli/tegraflash/rcm/device_test.go
git commit -m "feat(rcm): add ControlRead, RCMState, IsT264 for T23x state probe"
```

---

### Task 3: T23x multi-image loader

Implement `LoadImagesT23x`, which probes the bootROM state and sends each image as a separate RCM40 bulk write. Also add a unit test verifying `BuildDLMiniloader` produces a valid T264 message.

**Files:**
- Create: `internal/cli/tegraflash/rcm/t23x.go`
- Create: `internal/cli/tegraflash/rcm/t23x_test.go`

**Interfaces:**
- Consumes:
  - `BuildDLMiniloader(payload []byte, args [48]byte) (Message, error)` from `rcm/message.go`
  - `(d *Device) RCMState() (byte, error)` from Task 2
  - `(d *Device) Write(buf []byte) error` from `rcm/device.go`
  - `(d *Device) Read(buf []byte) (int, error)` from `rcm/device.go`
  - `VersionT234`, `CmdDLMiniloader`, `msgHeaderSize`, `msgOffOpcode`, `msgOffRCMVersion` from `rcm/constants.go`
- Produces:
  - `func LoadImagesT23x(dev *Device, images [][]byte) error`

---

- [ ] **Step 1: Write the failing test**

Create `internal/cli/tegraflash/rcm/t23x_test.go`:

```go
//go:build darwin || linux

package rcm

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestBuildDLMiniloaderT264 verifies that BuildDLMiniloader produces a valid
// RCM40 message for a T264-sized payload: correct opcode, version, payload
// placement, and a non-zero CMAC.
func TestBuildDLMiniloaderT264(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 512)
	msg, err := BuildDLMiniloader(payload, [48]byte{})
	if err != nil {
		t.Fatalf("BuildDLMiniloader() error = %v", err)
	}

	// Total length: max(1024, 644+512) = 1156 → pad to 16-byte boundary = 1168.
	wantLen := 1168
	if len(msg) != wantLen {
		t.Errorf("len(msg) = %d, want %d", len(msg), wantLen)
	}

	// Opcode must be CmdDLMiniloader (0x4).
	opcode := binary.LittleEndian.Uint32(msg[msgOffOpcode:])
	if opcode != CmdDLMiniloader {
		t.Errorf("opcode = %#x, want %#x (CmdDLMiniloader)", opcode, CmdDLMiniloader)
	}

	// RCM version must be Version40 (0x00400001).
	ver := binary.LittleEndian.Uint32(msg[msgOffRCMVersion:])
	if ver != VersionT234 {
		t.Errorf("rcm_version = %#x, want %#x (Version40/VersionT234)", ver, VersionT234)
	}

	// Payload must start at offset 0x284 (msgHeaderSize).
	if !bytes.Equal(msg[msgHeaderSize:msgHeaderSize+512], payload) {
		t.Error("payload bytes at offset 0x284 do not match input")
	}

	// len_insecure must equal total message length.
	lenInsecure := binary.LittleEndian.Uint32(msg[0:])
	if lenInsecure != uint32(wantLen) {
		t.Errorf("len_insecure = %d, want %d", lenInsecure, wantLen)
	}

	// payload_len must equal the payload size.
	payloadLen := binary.LittleEndian.Uint32(msg[msgOffPayloadLen:])
	if payloadLen != 512 {
		t.Errorf("payload_len = %d, want 512", payloadLen)
	}

	// CMAC at offset 0x104 must be non-zero (computed over a non-trivial message).
	cmac := msg[msgOffObjectSig : msgOffObjectSig+16]
	if bytes.Equal(cmac, make([]byte, 16)) {
		t.Error("CMAC at 0x104 is all-zero; expected a computed value")
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

```bash
go test ./internal/cli/tegraflash/rcm/ -run TestBuildDLMiniloaderT264 -v
```

Expected: `FAIL — undefined: msgOffPayloadLen` (or the test fails on the length assertion if those constants exist). The test should fail for a real reason, not compile.

If it fails to compile on an undefined constant, check `rcm/constants.go` — `msgOffObjectSig` and `msgOffPayloadLen` are defined there. If the test compiles but fails, that is also fine; record the actual failure reason before proceeding.

- [ ] **Step 3: Implement `LoadImagesT23x`**

Create `internal/cli/tegraflash/rcm/t23x.go`:

```go
//go:build darwin || linux

package rcm

import "fmt"

// LoadImagesT23x performs the T23x multi-image RCM download sequence used by
// T264 (Thor) devices. It probes the bootROM state via USB control transfer,
// then sends each image as a separate RCM40 DL_MINILOADER bulk write.
//
// images must be provided in bootROM-required order (mb1, psc_bl1, applet).
// The caller extracts the image list from Bundle.RCMImages().
//
// Protocol derived from RE of tegrarcm_v2 mainT23x (Thor nightly 20260618).
func LoadImagesT23x(dev *Device, images [][]byte) error {
	state, err := dev.RCMState()
	if err != nil {
		return fmt.Errorf("probing T23x RCM state: %w", err)
	}
	if state != 0 {
		return fmt.Errorf("unexpected T23x RCM state %d (want 0): power-cycle the device and retry", state)
	}

	for i, img := range images {
		msg, err := BuildDLMiniloader(img, [48]byte{})
		if err != nil {
			return fmt.Errorf("building RCM40 message for image %d: %w", i, err)
		}
		if err := dev.Write(msg); err != nil {
			return fmt.Errorf("sending image %d via RCM40: %w", i, err)
		}
		status := make([]byte, 4)
		if _, err := dev.Read(status); err != nil {
			// The applet (final image) causes an immediate device reset; the
			// bootROM may not send a status word before the USB connection drops.
			// Treat a read error only on the last image as success.
			if i < len(images)-1 {
				return fmt.Errorf("reading status after image %d: %w", i, err)
			}
		}
	}
	return nil
}
```

- [ ] **Step 4: Run both rcm package tests**

```bash
go test ./internal/cli/tegraflash/rcm/ -v
```

Expected:
```
--- PASS: TestParseStateDescriptor/state_0_initial (0.00s)
--- PASS: TestParseStateDescriptor/state_5_MB2_applet_running (0.00s)
--- PASS: TestParseStateDescriptor/state_8_MB2_running (0.00s)
--- PASS: TestParseStateDescriptor/n=2_too_short (0.00s)
--- PASS: TestParseStateDescriptor/n=0_empty_read (0.00s)
--- PASS: TestBuildDLMiniloaderT264 (0.00s)
PASS
```

- [ ] **Step 5: Verify the build**

```bash
go build ./...
```

Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/tegraflash/rcm/t23x.go internal/cli/tegraflash/rcm/t23x_test.go
git commit -m "feat(rcm): add LoadImagesT23x for T264 multi-image RCM sequence"
```

---

### Task 4: Flash coordinator dispatch

Wire the T264 path into `flash.go`. When the connected device is a T264, use `Bundle.RCMImages()` + `rcm.LoadImagesT23x()` instead of the single-applet `dev.LoadApplet()`.

**Files:**
- Modify: `internal/cli/tegraflash/flash.go`

**Interfaces:**
- Consumes:
  - `(d *Device) IsT264() bool` from Task 2
  - `func LoadImagesT23x(dev *Device, images [][]byte) error` from Task 3
  - `(b *Bundle) RCMImages() ([]RCMImage, error)` from Task 1
  - `(b *Bundle) ExtractFile(name string) ([]byte, error)` — already present

---

- [ ] **Step 1: Replace the applet-load section in `flash.go`**

Find the existing block (around line 91):

```go
fmt.Fprintln(out, "Loading applet via RCM...")
if err := dev.LoadApplet(applet); err != nil {
    dev.Close()
    return fmt.Errorf("loading applet: %w", err)
}
fmt.Fprintln(out, "  Applet sent; waiting for nv3p interface...")
```

Replace it with:

```go
if dev.IsT264() {
    fmt.Fprintln(out, "Loading images via T23x RCM sequence...")
    rcmImages, err := b.RCMImages()
    if err != nil {
        dev.Close()
        return fmt.Errorf("loading T264 RCM image list: %w", err)
    }
    var binaries [][]byte
    for _, img := range rcmImages {
        data, imgErr := b.ExtractFile(img.Filename)
        if imgErr != nil {
            fmt.Fprintf(out, "  [skip] %s (%s): not in bundle\n", img.Name, img.Filename)
            continue
        }
        fmt.Fprintf(out, "  queuing %s: %d bytes\n", img.Name, len(data))
        binaries = append(binaries, data)
    }
    if len(binaries) == 0 {
        dev.Close()
        return fmt.Errorf("no T264 RCM images found in bundle")
    }
    if err := rcm.LoadImagesT23x(dev, binaries); err != nil {
        dev.Close()
        return fmt.Errorf("T264 RCM sequence: %w", err)
    }
} else {
    fmt.Fprintln(out, "Loading applet via RCM...")
    if err := dev.LoadApplet(applet); err != nil {
        dev.Close()
        return fmt.Errorf("loading applet: %w", err)
    }
}
fmt.Fprintln(out, "  Applet sent; waiting for nv3p interface...")
```

Also remove the `fmt.Fprintf(out, "  applet_t234.bin: %d bytes\n", len(applet))` line (around line 60) and the `applet` variable extraction for the T264 path — the `b.Applet()` call remains but only for the T234 branch log. Because T264 no longer uses the `applet` variable at the top of the function, move the `b.Applet()` call inside the `else` branch.

Specifically, find this block near the top of `Flash()`:

```go
applet, err := b.Applet()
if err != nil {
    return fmt.Errorf("extracting applet: %w", err)
}
fmt.Fprintf(out, "  applet_t234.bin: %d bytes\n", len(applet))
```

The cleanest approach is to leave `b.Applet()` where it is (it returns `applet_t264.bin` for Thor bundles anyway and is cheap), but only use `applet` inside the `else` branch. The log line for applet size should also move inside the `else` branch. After the change, `applet` is only referenced inside `else`:

```go
applet, err := b.Applet()
if err != nil {
    return fmt.Errorf("extracting applet: %w", err)
}
// applet used only in the T234 (else) branch below
```

This avoids adding complex early-return logic. The variable is declared but used only inside the branch — Go accepts this as long as it is referenced.

- [ ] **Step 2: Verify the build compiles**

```bash
go build ./...
```

Expected: no output. If you get `applet declared and not used`, move the `b.Applet()` call into the `else` block fully:

```go
} else {
    applet, err := b.Applet()
    if err != nil {
        dev.Close()
        return fmt.Errorf("extracting applet: %w", err)
    }
    fmt.Fprintln(out, "Loading applet via RCM...")
    if err := dev.LoadApplet(applet); err != nil {
        dev.Close()
        return fmt.Errorf("loading applet: %w", err)
    }
}
```

And remove the top-level `applet, err := b.Applet()` block and its log line entirely.

- [ ] **Step 3: Run all package tests**

```bash
go test ./internal/cli/tegraflash/... -v
```

Expected: all existing tests pass, no new failures.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/tegraflash/flash.go
git commit -m "feat(flash): dispatch T264 to T23x multi-image RCM path"
```

---

## Post-implementation: manual verification checklist

These steps require a live T264 (AGX Thor) device. They are not automated.

- [ ] Power-cycle the device into USB Recovery Mode (hold REC, press RESET, release REC after 2 s).
- [ ] Confirm the device enumerates at `0x0955:0x7026` (`lsusb` or `system_profiler SPUSBDataType`).
- [ ] Run `wendy tegraflash flash --bundle <path-to-thor-bundle>` (or equivalent invocation).
- [ ] Confirm output shows `queuing mb1`, `queuing psc_bl1`, `queuing mb2-applet` (or equivalent names from the XML).
- [ ] Confirm nv3p interface appears after RCM phase and QSPI partitions write successfully.
- [ ] If `RCMState()` returns an unexpected value, capture USB traffic on Linux with `usbmon` and compare against the known descriptor layout to verify the state byte offset.
