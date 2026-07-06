# WendyCom Protocol

## Basics

WendyCom is a lightweight request/response protocol used to control Wendy Lite devices. It runs over two transports: USB (via the ESP32 USB Serial JTAG peripheral) and mTLS (over the network).

The host sends a `WendyComCommand` and the device replies with a `WendyComResponse`. Each command carries a `request_id` that the response echoes back, allowing the host to match replies to outstanding requests. The command type is encoded as a `oneof` field in the protobuf message.

Each message consists of an 8-byte header followed by a protobuf-encoded body.

### Message Header

|Offset|Field|Type|Description|
|------|-----|----|-----------|
|0|`magic`|`uint8_t`|Fixed sync byte `0xA5`, used to detect framing errors|
|1|`version`|`uint8_t`|Protocol version, currently always `1`|
|2|`category`|`uint8_t`|Message category; only `0` is accepted (non-zero triggers a receive reset)|
|3|`channel`|`uint8_t`|Sub-channel within the link|
|4–5|`reserved`|`uint16_t`|Unused, set to `0` on send|
|6–7|`body_size`|`uint16_t`|Length of the body that follows, in network byte order|

## WendyCom over TLS

The device listens on TCP port 5054 and advertises itself via mDNS as `_wendy-lite._tcp`. Every connection is TLS-encrypted. The device operates in one of two modes depending on whether it has been enrolled in Wendy Cloud:

- **Unauthenticated TLS** — When not enrolled, the device uses a built-in default certificate; client identity is not verified. This is the factory default. The mDNS TXT record advertises `mtls=false`.
- **mTLS** — When enrolled, the device uses a provisioned certificate and requires the client to present a certificate signed by the configured chain of trust. The mDNS TXT record advertises `mtls=true`.

## WendyCom over USB Serial JTAG

WendyCom runs over the ESP32 USB Serial JTAG peripheral. This link supports several modes, WendyCom being one of them; the host switches between them by sending ESC sequences.

### Modes

**`USJ_MODE_OFF` (0)** — USB input is silently discarded.  No data is written to the channel.

**`USJ_MODE_CONSOLE` (1)** — ESP log output is mirrored to the USB channel (the `vprintf`-hook forwards every log line).  `wendy_usj_write()` is active.  This is the startup default.

**`USJ_MODE_ECHO` (2)** — Every received byte is immediately echoed back.  Useful for connectivity checks.

**`USJ_MODE_COM` (3)** — The channel is handed to the `wendy_com` stack, enabling WendyCom message exchange over USB as we do over mTLS.

### Escape sequences

`ESC` (0x1B) characters in the data stream are interpreted as commands. Each `ESC` is followed by a command byte:

```text
ESC c  →  switch to console mode
ESC e  →  switch to echo mode
ESC m  →  switch to com mode
ESC o  →  switch to off mode
```

In `USJ_MODE_COM`, the `wendy_com_uart` layer intercepts escapes before data reaches the `wendy_com` stack:

```text
ESC _    →  pass a literal ESC byte through to the `wendy_com` stack
ESC k    →  keep-alive (reserved, not yet implemented)
ESC <x>  →  disconnect the `wendy_com` link, then switch to mode <x>
```

Two consecutive `ESC` characters are considered like one. Therefore, you can put as many `ESC` as you want in front of a command byte.

### Establishing a WendyCom connection

Before entering `USJ_MODE_COM`, the host must flush any stale data buffered in the
USB layer on both sides.  The handshake uses echo mode for this:

1. Open the USB channel.
2. Send `ESC e` to switch to echo mode.
3. Verify the link: send a few bytes and confirm that data flows back.
4. Drain the channel: read until no data arrives within a timeout.
5. Send a unique sentinel byte sequence and wait until it is echoed back —
   this confirms the channel is fully flushed and both sides are in sync.
6. Send `ESC m` to switch to `USJ_MODE_COM`.

The channel is now in `USJ_MODE_COM` with no stale data in either buffer.

### Switching to program mode

In addition to the modes described above, the host can reset the device into program mode via the DTR and RTS signals exposed by the USB Serial JTAG peripheral. The `wendy os install` command uses this to flash the firmware.
See [wendy os install](../clients/wendy-cli/commands/os/install.md)

### Caveat

USB Serial JTAG currently has no flow control. If the host sends data faster than the device can consume it, bytes may be silently dropped. To avoid this, messages must be kept below 1024 bytes, and the host must wait for a response before sending the next message.

## WendyCom Commands

### ProtocolVersion

Sent automatically when a connection is established. The host declares the protocol version it supports (`major=1, minor=0`); the device replies with the version it negotiated. The connection is rejected if the result is not `OK`.

### Ping

Checks that the device is reachable and responsive. The device replies with `OK` and no payload.

### Reboot

Asks the device to reboot. The device replies with `OK` before resetting.

### App Download

Transfers a WASM application binary to the device in three steps:

1. **AppPushBegin** — opens a transfer session and tells the device the total file size in bytes.
2. **AppPushData** — sends the binary in chunks, each carrying its byte offset within the file. The host sends one command per chunk and waits for `OK` before sending the next.
3. **AppPushEnd** — closes the transfer session, signalling that all chunks have been delivered.

### AppStart

Starts the previously downloaded WASM application on the device.

### AppStop

Stops the currently running application.

### GetDeviceIdentity

Queries the device for its identity. The device responds with a `WendyComDeviceIdentity` message containing three fields:

- `id` — a unique identifier for the device (e.g. a serial number or hardware ID).
- `name` — a short machine-readable name.
- `display_name` — a human-readable display name.

If the device cannot provide an identity, it returns an error result and the host discards the device from its list of reachable devices.
