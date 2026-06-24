# T264 (Thor) RCM and nv3p protocol implementation

Date: 2026-06-18
Status: approved

## Problem

The existing Go implementation (`internal/cli/tegraflash/`) flashes Jetson devices via USB
Recovery Mode (RCM). It works for T234 (Orin, USB PID 0x7023) but fails for T264 (Thor, USB
PID 0x7026). Both chips belong to the T23x family and share the same USB bootROM protocol, but
T264's bootROM requires a specific USB control-transfer handshake and a multi-image download
sequence that the current code does not implement.

This document covers the protocol design derived from reverse-engineering `tegrarcm_v2`
(built for Thor nightly 20260618, ELF 32-bit x86, 831 functions, GCC 4.6, not stripped) and
from inspecting the tegraflash bundle at
`jetson-agx-thor-nightly-20260618T164033-recovery.tegraflash.tar.gz`.

---

## Background: chip families and USB identifiers

NVIDIA assigns USB Product Identifiers using the scheme `0x70XX` where `XX` is the lower byte
of the chip ID. All Jetson chips use Vendor ID 0x0955.

| Chip family | USB PID | Notes                      |
|-------------|---------|----------------------------|
| T18x/Xavier | 0x7018  | legacy, not targeted here  |
| T19x/Orin   | 0x7019  | legacy, not targeted here  |
| T21x/TX2    | 0x7021  | legacy, not targeted here  |
| T234/Orin   | 0x7023  | current implementation works |
| T264/Thor   | 0x7026  | fails, target of this work |

The chip ID byte also doubles as a value embedded in the RCM version word (see below).

---

## Part 1: RCM40 message format

This applies to both T234 and T264. The message is sent over USB bulk OUT.

### Header layout (644 bytes)

```
Offset  Size  Field
0x000   4     len_insecure — total message length (header + payload, AES-padded)
0x004   256   rsa_modulus — zero for ODM-open devices
0x104   16    cmac_hash — AES-128-CMAC of [0x104..end] with all-zero key (ODM-open)
0x114   256   rsa_sig — zero for ODM-open devices
0x214   16    reserved — zero
0x224   16    ecid — chip unique ID (zero acceptable; used for targeted signing)
0x234   4     opcode — command code
0x238   4     len_secure — same as len_insecure for ODM-open
0x23c   4     payload_len — length of the binary payload following the header
0x240   4     rcm_version — (major << 16) | minor; see table below
0x244   48    args — command-specific arguments, zero for DL_MINILOADER
0x274   16    padding — zero to reach 644-byte (0x284) boundary
0x284   ...   payload — applet or other binary
```

The total message is padded to a multiple of 16 bytes, minimum 1024 bytes.

### RCM opcode values

| Name              | Value | Description                   |
|-------------------|-------|-------------------------------|
| CmdNone           | 0x0   | no-op                         |
| CmdSync           | 0x1   | synchronise with bootROM      |
| CmdDLMiniloader   | 0x4   | download miniloader (applet)  |
| CmdQueryBRVersion | 0x5   | query Boot ROM version        |
| CmdQueryRCMVersion| 0x6   | query RCM version             |
| CmdQueryBDVersion | 0x7   | query BD version              |

All T23x (T234, T264) image downloads use CmdDLMiniloader (0x4).

### RCM version values

| Constant    | Encoded value    | Used for          |
|-------------|------------------|-------------------|
| Version1    | 0x00010000       | T18x              |
| Version35   | 0x00350001       | T19x              |
| Version40   | 0x00400001       | T23x (T234, T264) |

T264 uses Version40, the same as T234.

### Signing for ODM-open devices

1. Zero the rsa_modulus and rsa_sig fields.
2. Compute AES-128-CMAC over `msg[0x104..end]` using the all-zero 16-byte key (RFC 4493).
3. Store the 16-byte CMAC tag at `msg[0x104]`.

The existing `message.go` implementation is correct and requires no changes.

### USB bulk write framing

Send the full message as a single bulk OUT write. If the message length is exactly divisible by
the USB endpoint's maximum packet size (512 bytes for USB 3.0, 64 bytes for USB 2.0), follow it
with a zero-length packet to signal end-of-transfer.

After writing, read back a 4-byte status word. A status of 0 means the bootROM accepted the
image. The device may reset before sending status; treat a read timeout as success.

---

## Part 2: T23x download sequence (T264-specific)

> **Correction (2026-06-24, validated on live T264 over macOS).** An earlier draft of this
> section described an "RCM state machine" (states 0–8) read from USB string descriptor index 3.
> That was a misreading. Two corrections:
>
> 1. **There is no device-side state gate.** `tegrarcm_v2`'s `GetRcmState` reads a *host-side
>    local file* (`rcm_state`) for cross-invocation bookkeeping — it is not a device protocol
>    step. `tegrarcm_v2` issues `--new_session`
>    and then downloads images immediately, with no state query.
> 2. **String descriptor index 3 is the chip BR_CID, not a state.** It returns the BR_CID hex
>    string with its characters reversed. Read on a live T264 over macOS:
>    `0C08FF61…1008`, which reversed is `80012641783DE2442400000016FF80C0` — exactly the BR_CID
>    that `tegrarcm_v2 --uid` reports. This is now `Device.ReadChipID`, used only to identify the
>    chip / recover the ECID on macOS (where the bulk-IN UID read is dropped by IOKit).
>
> The actual T264 download order (from the flash log) is `bct_br → mb1 → psc_bl1 → bct_mb1 →
> applet`, BCTs first. The rest of this section is retained for the descriptor mechanics.

T234 accepts a single RCM40 message (the applet) in its open-mode state. T264 requires the
multi-image sequence above.

### USB control transfer: reading the chip BR_CID (string descriptor 3)

The chip BR_CID is read with the standard `GET_DESCRIPTOR` request for string descriptor 3.
(This descriptor was previously, and incorrectly, thought to encode an RCM state.)

| Field         | Value  | Meaning                              |
|---------------|--------|--------------------------------------|
| bmRequestType | 0x80   | IN, standard, device                 |
| bRequest      | 0x06   | GET_DESCRIPTOR                       |
| wValue        | 0x0303 | descriptor type STRING (0x03), index 3 |
| wIndex        | 0x0000 | language ID 0                        |
| wLength       | 0x0060 | 96 bytes maximum                     |

The response follows the standard USB string descriptor layout:

```
Byte 0: bLength — total length including this header
Byte 1: bDescriptorType = 0x03
Byte 2+: UTF-16LE encoded payload (low byte of each code unit = one ASCII hex char)
```

Taking the low byte of each UTF-16LE code unit yields the BR_CID hex string *reversed*; reverse
it to recover the BR_CID. The ECID is the BR_CID with the leading chip/SKU identifier removed
(e.g. BR_CID `0x80012641783DE2442400000016FF80C0` → ECID `0x1783DE2442400000016FF80C0`). No
"state" is encoded here.

### Pre-applet image sequence (state 0)

With state 0, download the following images from the bundle in order, each as a separate RCM40
DL_MINILOADER bulk write:

| tegrarcm type | Bundle file             | Required |
|---------------|-------------------------|----------|
| mb1           | mb1_t264_prod.bin       | yes      |
| psc_bl1       | psc_bl1_t264_prod.bin   | yes      |
| applet        | applet_t264.bin         | yes      |
| bct_br        | (compiled from .cfg)    | skip if absent |
| bct_mb1       | (compiled from .cfg)    | skip if absent |
| bct_mem       | (compiled from .cfg)    | skip if absent |

The image ordering is determined by the `rcmboot-flash.xml.in` file in the tegraflash bundle
(`device type="rcm"` block). Any partition with an empty `<filename>` is skipped.

After the last image, the device re-enumerates as an nv3p endpoint.

### Post-applet image sequence (state 5+)

In the context of WendyOS flashing, we do not enter the post-applet image download phase. QSPI
and UFS partition writes happen via the nv3p protocol once the applet (MB2) is running.

---

## Part 3: nv3p protocol

The nv3p (NVIDIA 3-Party) protocol runs on top of USB bulk transfers after the applet has loaded.
It is already implemented in `internal/cli/tegraflash/nv3p/`. This section documents it for
reference.

### Packet structure

All integers are little-endian. The checksum is the two's complement of the sum of all bytes in
the packet's payload (sum all bytes, then negate: `checksum = ^sum + 1`).

**Command packet:**
```
version   uint32  — always 1
type      uint32  — 1 (CMD)
sequence  uint32  — monotonically increasing per session
args_len  uint32  — length of args field in bytes
command   uint32  — command code (see below)
args      []byte  — command-specific arguments
checksum  uint32
```

**Data packet:**
```
version   uint32  — always 1
type      uint32  — 2 (DATA)
sequence  uint32
data_len  uint32
data      []byte
checksum  uint32
```

**ACK packet:**
```
version   uint32  — always 1
type      uint32  — 4 (ACK)
sequence  uint32
checksum  uint32
```

**NACK packet:**
```
version   uint32  — always 1
type      uint32  — 5 (NACK)
sequence  uint32
error_code uint32
checksum  uint32
```

### nv3p command codes

| Name              | Code | Args layout                                      |
|-------------------|------|--------------------------------------------------|
| CmdGetPlatformInfo| 0x01 | none; reply: 80-byte PlatformInfo struct         |
| CmdGetBCT         | 0x02 | none                                             |
| CmdDLBCT          | 0x04 | none; followed by data packet                    |
| CmdDLBL           | 0x06 | none; followed by data packet                    |
| CmdDLPartition    | 0x08 | uint64 length, uint32 id, uint32 type            |
| CmdStatus         | 0x0a | none                                             |
| CmdReset          | 0x0e | none                                             |

### USB raw frame

`tegrarcm_v2` sends nv3p packets over a 16-byte header + data framing:
```
Byte 0-1:  sequence number (uint16 LE), always 3 for the frame header
Byte 2:    packet type (low byte)
Byte 3:    packet type (high byte)
Byte 4-5:  payload length low word
Byte 6-7:  payload length high word
```
The 16-byte frame header is written as a single USB bulk write, then the data follows. Checksum
is accumulated across all bytes. Timeout per transfer: 1 second (0xF4240 microseconds).

---

## Part 4: Implementation design

### Files changed

| File                                  | Change                                      |
|---------------------------------------|---------------------------------------------|
| `internal/cli/tegraflash/rcm/device.go`    | add `ControlRead`, `ProductID`, `IsT264`    |
| `internal/cli/tegraflash/rcm/bootrom.go`  | new — `DownloadBootROMImages` (was `t23x.go`/`LoadImagesT23x`) |
| `internal/cli/tegraflash/bundle/xml.go`    | add `RCMPartitions` (parse rcmboot XML)     |
| `internal/cli/tegraflash/flash.go`         | chip dispatch: T264 → T23x path             |

### `rcm/device.go` additions

```go
// ProductID returns the USB product ID of the connected device.
func (d *Device) ProductID() gousb.ID {
    return d.dev.Desc.Product
}

// IsT264 reports whether the device is a T264 (Thor) chip.
func (d *Device) IsT264() bool {
    return d.ProductID() == ProductThor
}

// ControlRead reads a USB string descriptor from the bootROM.
// T23x bootROMs encode RCM state in string descriptor index 3.
func (d *Device) ControlRead(buf []byte) (int, error) {
    return d.dev.Control(
        0x80,   // bmRequestType: IN, standard, device
        0x06,   // bRequest: GET_DESCRIPTOR
        0x0303, // wValue: STRING descriptor (0x03), index 3
        0x0000, // wIndex: language 0
        buf,
    )
}

// ReadChipID reads the chip BR_CID from string descriptor 3 (returned reversed).
// Implemented; no device "state" is read. See rcm/device.go for the real version.
func (d *Device) ReadChipID() (string, error) { /* ... */ }
```

### `rcm/bootrom.go` (was `t23x.go`)

`LoadImagesT23x` was renamed `DownloadBootROMImages` — "T23x" is NVIDIA's bootROM family
covering both T234/Orin and T264/Thor, so the old name read as Orin-specific. Each image is
sent **verbatim** (no `BuildDLMiniloader` wrapper); `Device.Write` does the 16 KiB chunking and
trailing ZLP. No state gate.

```go
func DownloadBootROMImages(dev *Device, images [][]byte) error {
    drainBulkIn(dev) // consume the connect-time ECID before the first bulk OUT
    for i, img := range images {
        if err := dev.Write(img); err != nil { // verbatim; Device.Write chunks + ZLP
            return fmt.Errorf("sending bootROM image %d: %w", i, err)
        }
        status := make([]byte, 4)
        if _, err := dev.Read(status); err != nil {
            return fmt.Errorf("reading status after bootROM image %d: %w", i, err)
        }
    }
    return nil
}
```

### `bundle/xml.go` addition

```go
// RCMImage is one entry from the rcmboot-flash.xml.in rcm device block.
type RCMImage struct {
    Name     string
    Type     string
    Filename string
}

// RCMImages parses rcmboot-flash.xml.in from the bundle and returns the ordered
// list of images with non-empty filenames from the device type="rcm" block.
func (b *Bundle) RCMImages() ([]RCMImage, error) { ... }
```

### `flash.go` dispatch

```go
dev, err := rcm.WaitForDevice()
// ...

if dev.IsT264() {
    rcmImages, err := b.RCMImages()
    // ...
    var binaries [][]byte
    for _, img := range rcmImages {
        data, err := b.ExtractFile(img.Filename)
        // skip if not found (e.g. BCT placeholders)
        if err != nil { continue }
        binaries = append(binaries, data)
    }
    if err := rcm.DownloadBootROMImages(dev, binaries); err != nil {
        return fmt.Errorf("T264 RCM sequence: %w", err)
    }
} else {
    // T234 (Orin) — existing single-applet path
    if err := dev.LoadApplet(applet); err != nil { ... }
}
dev.Close()
```

### Error handling

- State != 0 on probe: return an error with the actual state value so the user knows the device
  needs a power cycle, not a reflash.
- Individual image load failures for BCT-type images (absent from bundle): log and skip, do not
  abort. Failures on mb1, psc_bl1, or applet: abort with a clear error message.
- RCM state descriptor read failure on macOS: gousb wraps libusb; on macOS, libusb can issue
  control transfers to unclaimed devices via the IOUSBLib. If this fails, surface the raw error
  and note that Linux may be required for T264 flashing until the macOS path is validated.

### Testing

- Unit: `TestBuildDLMiniloaderT264` verifying the RCM40 message layout and CMAC with a known
  vector.
- Unit: `TestRCMImages` verifying that `RCMImages()` correctly parses `rcmboot-flash.xml.in`
  and returns only images with non-empty filenames in the right order.
- Integration: live T264 device required. Use `cmd/thor-replay` with captured/generated
  artifacts in bootROM order (`bct_br mb1 psc_bl1 bct_mb1`); expected flow is each blob is
  accepted (status read OK) and the device then advances to the mb2 applet (nv3p) phase.

---

## Open questions

1. ~~**State byte offset**~~ **RESOLVED (2026-06-24).** Not a state — string descriptor 3 is the
   BR_CID (reversed). Validated on a live T264 over macOS; decode matches `tegrarcm_v2 --uid`.
   Now `Device.ReadChipID`; the state gate has been removed.

2. **Zero-length packet**: The current `Write` implementation does not send a ZLP after
   boundary-aligned writes. Legacy T234 works without it. T264 behaviour is unknown; if images
   fail to load, try adding ZLP handling.

3. ~~**macOS ControlRead**~~ **RESOLVED (2026-06-24).** The EP0 GET_DESCRIPTOR control transfer
   works on macOS IOKit — confirmed on a live T264 (66-byte descriptor returned). The bulk-IN UID
   read still fails on macOS ("transfer was cancelled"); use `ReadChipID` to recover the ECID.

4. **RCM version for T264**: Currently `VersionT264 = Version40`. If T264 requires a different
   RCM version word, capture USB traffic to verify.

5. ~~**Verbatim blob vs RCM40 wrapper**~~ **RESOLVED (2026-06-24).** Now sent verbatim:
   `LoadImagesT23x` was renamed `DownloadBootROMImages` and no longer wraps via
   `BuildDLMiniloader`; `Device.Write` got the required 16 KiB chunking + trailing ZLP. Pending
   hardware confirmation via `cmd/thor-replay`.

6. **Session establishment (NEW)**: unverified whether `--new_session` needs an explicit
   Sync/QueryRcmVersion message before the image downloads. `DownloadBootROMImages` currently
   sends only the blobs (after draining the connect-time bulk-IN). Confirm during replay.
