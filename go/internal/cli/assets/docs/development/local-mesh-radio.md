# ESP32 mesh leaves over BLE

The optional BLE leaf provider connects Linux routers to enrolled Wendy Lite
endpoints without requiring those endpoints to join a Wi-Fi access point.
It is separate from the existing router-to-router BLE provider. TCP leaves and
other mesh carriers remain available.

Add `leafBLEPeers` and `leafTimeURL` to the router's existing `local-mesh.json`:

```json
{
  "leafBLEPeers": [{
    "device": "12345678-9abc-4def-8123-000000000002",
    "address": "80:45:6b:00:00:01",
    "addressType": "public",
    "psm": 129
  }],
  "leafTimeURL": "https://your-enrolled-timestamp-authority.example/timestamp"
}
```

Replace the example values with the leaf's enrolled UUID, Bluetooth address and
signed-time authority URL. The router uses its system HTTPS trust store for the
authority. Preserve existing carriers and peers. Linux needs Bluetooth hardware
and permission to open LE L2CAP sockets.

The ESP32 build must select `WENDY_MESH_BLE`, reserve at least two NimBLE CoC
channels, and use the same PSM. Its `WENDY_MESH_ROUTER_DEVICE` pins the router's
enrolled UUID. The default mesh PSM is 129; WendyCom management uses 128.
Bluetooth addresses locate peers; they do not authorize identities.

The Linux router dials the ESP32's L2CAP listener but remains the TLS server.
Before TLS it relays one bounded RFC 3161 timestamp request to `leafTimeURL`.
The endpoint verifies the authority's signature, its random nonce and its
persisted clock floor itself. TLS then authenticates both device principals
with `wendy-local-mesh-leaf/3`. Existing signed-route, replay and packet-source
checks apply unchanged. No caller-supplied URL, credentials or unsigned clock
value is accepted by the timestamp relay.

At most eight explicitly configured leaves are dialed, with one retry worker
per identity. Carrier readiness means the workers are running. Inspect node
links to check that a peer actually authenticated. Automatic discovery and
rotating Bluetooth addresses are not supported. The leaf retains its existing
125-second cold-boot replay quarantine and five-minute fresh-time limit.

This is a BLE carrier for Wendy's IP mesh, not the Bluetooth SIG Mesh protocol.
Wendy Lite also supports a separate direct ESP-NOW link between two ESP32-S3
endpoints. Linux does not transmit ESP-NOW frames in this implementation.
Lite still advertises only its own IPv6 /128 and does not forward packets for
other endpoints.

Validation covers timestamp framing and bounds, refusal of redirects and
non-HTTPS URLs, cancellation, configuration validation and existing TLS and
signed-leaf checks. The ARM64 agent builds. BLE operation with a physical ESP32
still requires hardware validation.
