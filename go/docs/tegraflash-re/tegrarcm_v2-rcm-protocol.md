# tegrarcm_v2 — USB RCM protocol (T264 / Thor, chip 0x26)

Reverse-engineered from `tegrarcm_v2` (ELF 32-bit i386, not stripped, with debug_info,
Thor nightly 20260618) and validated against a live AGX Thor (USB 0x0955:0x7026) during
the Go port. This documents the bootROM-stage USB Recovery Mode (RCM) protocol: how the
host talks to the Thor bootROM to download the boot chain.

## USB identifiers and enumeration

| Chip        | USB VID:PID   | Notes                          |
|-------------|---------------|--------------------------------|
| T234 (Orin) | 0x0955:0x7023 | existing Go path works         |
| T264 (Thor) | 0x0955:0x7026 | this document                  |

On a live Thor the recovery interface enumerates as:

```
speed=high (USB 2.0)   interface 0, alt 0
ep 0x81  IN  bulk  maxpacketsize 512
ep 0x01  OUT bulk  maxpacketsize 512
```

`NvTegraOpenUsb` is a pure Linux `usbfs` implementation: it opens
`/dev/bus/usb/<bus>/<dev>`, reads and parses the descriptor for the interface number
and the two bulk endpoints, then issues a single `ioctl(USBDEVFS_CLAIMINTERFACE)`
(`0x8004550f`). It does **not** issue `SET_CONFIGURATION`, `SET_INTERFACE`, or any
control transfer before bulk traffic. The Go port must do the same minimal setup; an
extra `SET_INTERFACE` is unnecessary and was confirmed to make no difference.

### UID read

`NvTegraReadUid` → `NvTegraRcmReadChipUid` simply performs
`NvTegraUsbReadTimeout(dev, buf, 16, 5000ms)` — a bare 16-byte bulk IN read with no
preceding write. T234 emits its UID on connect so this read succeeds; **T264 does not
send a UID at connect time**, so the read times out and the flow continues without it.
The UID is informational for ODM-open devices. In Go, do not pre-submit a UID read for
T264: a cancelled bulk IN transfer is pointless (the device sends nothing) though it is
not itself the cause of any failure.

## Bulk write framing — 16 KiB chunking (REQUIRED)

`NvTegraUsbWriteTimeout` splits every bulk OUT into chunks of **at most 16384 bytes
(0x4000)** and issues one `ioctl(USBDEVFS_BULK)` per chunk in a loop:

```
chunk = min(remaining, 0x4000)
loop: ioctl(USBDEVFS_BULK, ep=0x01, len=chunk, timeout); ptr += n; remaining -= n
```

This matters on macOS: handing libusb/IOKit a single multi-hundred-KiB bulk OUT
transfer fails immediately with `LIBUSB_TRANSFER_ERROR` / `kIOReturnNotResponding`
(`0xe00002ed`). The Go `Device.Write` must chunk to 16 KiB to match.

### Zero-length packet

After the chunk loop, `NvTegraRcmInitBootRomCommunication` checks the port speed and the
total length: if the message length is an exact multiple of the endpoint max packet size
(512 for high-speed bulk), it sends a **zero-length packet** to mark end-of-transfer.
The Go `Device.Write` replicates this (`len % maxPacketSize == 0` → trailing ZLP).

## Images are pre-signed "NVDA" RCM blobs — sent VERBATIM

This was the central correctness bug in the first Go attempt. The bootROM-stage images
shipped in the bundle are **already complete, signed RCM message blobs**. They begin with
the 4-byte ASCII magic **`NVDA`** (`4e 56 44 41`):

```
mb1_t264_prod.bin      00000000: 4e56 4441 5cfc c901 ...   NVDA....
psc_bl1_t264_prod.bin  00000000: 4e56 4441 ed7f 81a2 ...   NVDA....
```

(The `NVDA` header is the signed-image header produced by `tegrahost_v2
--appendsigheader`; see `tegrahost_v2.md` for its 8192-byte layout.)

In `RcmDownLoadImages` the download handler:
1. calls `GetRcmState` (which reads a **local state file** `rcm_state`, not the device —
   internal cross-invocation bookkeeping, not a device protocol step; the Go port does
   not need it),
2. reads the image file,
3. `memcmp`s the first 4 bytes against `"NVDA"` (sanity check),
4. writes the file contents **verbatim** with `NvTegraUsbWriteTimeout(buf, size, 10000ms)`.

There is **no** outer RCM40 / DL_MINILOADER envelope built around these blobs at this
stage. The original Go code wrapped each image in a hand-built RCM40 message via
`BuildDLMiniloader`; the doubly-wrapped result was rejected by the bootROM, which then
**physically reset the USB device** (observed as `darwin_devices_detached` +
`kIOReturnNotResponding` ~270 ms after the first chunk). Fix: send the blob bytes
unmodified.

> Note: `applet_t264.bin` is raw ARM code (starts `06 00 00 ea`), NOT an `NVDA` blob. The
> applet is delivered in a later phase over nv3p (`--pollbl --download applet`), not in
> the bootROM RCM phase.

## Bootrom download sequence (`--new_session`)

The Python orchestration (`tegraflash_impl_t264.tegraflash_send_to_bootrom`) invokes:

```
tegrarcm_v2 --new_session --chip 0x26 0 --uid \
    --download bct_br   <br_bct_BR.bct> \
    --download mb1      <mb1_t264_prod.bin> \
    --download psc_bl1  <psc_bl1_t264_prod.bin> \
    --download bct_mb1  <mb1_bct_MB1.bct>
```

Critical ordering facts:
- **`bct_br` is downloaded FIRST**, before mb1. The bootROM requires the BR BCT (Boot
  Configuration Table) to configure SDRAM and the boot chain before it will accept mb1.
- `bct_br` and `bct_mb1` are **not shipped pre-built** in the bundle; they are generated
  at flash time from device-tree config (see `bct-generation-orchestration.md` and
  `tegrabct_v2-br-bct-format.md`). This is the main reason the bootROM phase cannot be
  completed by simply sending the firmware files.

`NvTegraRcmInitBootRomCommunication` sends the message array in order; for each message it
writes (chunked, with ZLP), reads a 4-byte status word, and special-cases the opcode: a
QueryRcmVersion (opcode 6) message prints the version, a DL_MINILOADER (opcode 4) message
is followed by `sleep(1)`.

## Later phases (post-bootROM)

- **mb2 applet**: `tegraflash_send_mb2_applet` → `tegrarcm_v2 --pollbl --download applet
  <applet_t264.bin>`. Uses `NvTegraRcmAppletDownload`, which goes over the **nv3p**
  protocol (`NvTegra3pCommandSend` / `NvTegra3pSendFile`), not raw RCM. Polled for
  readiness via `--ismb2applet` / `--ismb2`.
- **membct + RCM blob**: `tegraflash_send_to_bootloader` → `--pollbl --download bct_mem
  <mem_bct> --download blob <blob>`.
- Partition writes happen over nv3p once MB2 is running (existing Go `nv3p` package).

## Implications for the Go port

1. `Device.Write`: 16 KiB chunking + conditional trailing ZLP. (Landed 2026-06-24 — the
   earlier "(Done.)" note predated the actual implementation; `Write` had been a single
   un-chunked `WriteContext`.)
2. `DownloadBootROMImages` (was `LoadImagesT23x`): send the blobs verbatim, no
   `BuildDLMiniloader`. (Landed 2026-06-24.)
3. The bootROM phase needs `bct_br` first and `bct_mb1` after psc_bl1 — both generated
   from BCT config. Generating them is the large remaining work (tegrabct_v2 port), or
   capture them from a real tegraflash run and replay (see `cmd/thor-replay`).
4. `BuildDLMiniloader` / the RCM40 envelope is not used for the T264 bootROM stage (it
   remains for the T234/Orin single-applet path). Keep it only if a query/sync message
   turns out to need it; the recorded T264 sequence sends the BCT/firmware blobs directly.
5. Not yet validated on hardware: whether `--new_session` requires an explicit Sync/
   QueryRcmVersion message before the downloads. `DownloadBootROMImages` currently sends
   only the image blobs; confirm during replay.
