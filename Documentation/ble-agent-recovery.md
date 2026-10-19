# BLE recovery for enrolled agents

The Linux agent accepts short management commands over Bluetooth Low Energy
when it has an enrolled device certificate. This lets a macOS or Linux CLI
repair Wi-Fi without an existing IP connection. Deployment and media continue
to use LAN or cloud connections.

Run `wendy device wifi list` or `wendy device wifi connect` and choose the BLE
device in the picker. Use credentials for the device's tenant. For commands
that support BLE, an unreachable named or saved-default device also triggers
a 15-second BLE recovery search. The CLI selects only a matching hostname or
saved device identity and retains the selected pin for TLS verification.
Recovery leaves the saved default unchanged. Identity or TLS authentication
refusals do not trigger this fallback.

This implementation does not enable first-boot claiming, unenrolled Wi-Fi
provisioning, Windows BLE connections, or a phone application. Native phone
clients can implement the discovery and command protocol below. They must use
the same certificate verification and device identity checks as the CLI.

## Discovery contract

The agent advertises service UUID `7565e9eb-4c20-4b67-9272-d708b397b631` through
BlueZ. The selected controller must support both LE advertising and a local
GATT server. `WENDY_BT_ADAPTER` can select a specific BlueZ adapter.
After a BlueZ restart, the agent retries registration for up to 30 seconds on
the same adapter path. It does not recover automatically if no adapter was
present at startup, the adapter is removed and re-enumerated, or restoration
exceeds that window. Restart the agent once the adapter is available in those
cases.

The service has one read-only characteristic,
`7565e9ec-4c20-4b67-9272-d708b397b631`. Its value is UTF-8 JSON, limited to
512 bytes. Read offsets are supported so clients can retrieve the value across
multiple ATT reads when necessary. Example:

```json
{
  "protocolVersion": 1,
  "psm": 128,
  "identity": "urn:wendy:org:7:asset:42",
  "hostname": "wendy-pi",
  "enrolled": true,
  "capabilities": ["wifi", "apps", "hardware", "bluetooth"]
}
```

`identity` can also be the certificate's canonical tenant SPIFFE principal.
It is a discovery hint, as are the hostname and advertisement. The client
must verify the certificate chain, tenant, and device identity when connecting.
A stored identity for a selected device takes precedence over discovery data.
A different advertised hostname must not bypass a stored identity check.

Version 1 requires an enrolled device and an LE dynamic PSM in the range
128 through 255. Clients ignore additional JSON fields but reject unsupported
protocol versions. The CLI offers only agents whose metadata it can read and
validate. Earlier agents that advertise the UUID without this GATT service
are not listed as compatible recovery targets.

## Authenticated command session

The client opens an LE credit-based L2CAP channel at the advertised PSM and
performs mutual TLS using its Wendy credentials. The agent applies the same
tenant authorization check as its LAN API before dispatching commands.

Inside TLS, each message has a two-byte unsigned big-endian payload length,
followed by a protobuf payload. Payloads must contain 1 through 65535 bytes.
Requests use `BluetoothCommand`; responses use `BluetoothResponse`, defined
in `Proto/wendy/agent/services/v1/wendy_agent_v1_bluetooth.proto`. A client sends
one request and reads its response before starting the next exchange. This
protocol has no request IDs and does not multiplex requests.

The stream adapter segments writes at the negotiated L2CAP SDU size and
retains unread bytes from each received SDU. Neither TLS records nor protobuf
frames need to align with SDU boundaries. A timeout or interrupted frame closes
the session; clients reconnect instead of reusing a partially consumed stream.
Do not automatically retry a mutating command whose response was lost.

TLS handshakes have a 15-second budget and command exchanges have a 60-second
budget. Platform connection stages and native macOS writes have their own
timeout budgets. Cancellation can wait for an in-flight native call to finish.

The Wi-Fi commands cover scanning, connection and status, disconnecting,
saved-network listing, forgetting, priorities, and reordering. Security and
hidden-network options have the same meanings as the corresponding LAN calls.
The protocol also retains agent version, basic app management, hardware listing,
and Bluetooth peripheral management.

After `wendy device wifi connect` succeeds over BLE, the CLI attempts LAN handoff
and authenticates the device against the identity observed during the BLE TLS
session. A matching hostname or mDNS advertisement alone is insufficient.
Wi-Fi connection and verified LAN reachability are separate outcomes, since a
network can isolate its clients. The interactive Wi-Fi table keeps using its
BLE session and does not perform this handoff.

## Validation before release

Automated tests cover metadata parsing, discovery, TLS authorization, framing,
interrupted sessions, named/default recovery, LAN handoff, and Wi-Fi dispatch.
Linux-specific tests must run on Linux. A macOS test run does not execute BlueZ
or Linux socket code.

The draft still needs radio validation on the supported board and client matrix:

- Start an enrolled agent with no working Wi-Fi. Discover it from macOS and Linux.
- Verify that a client from another tenant and a substituted device are rejected.
- Scan networks, connect with valid credentials, and verify the same device over LAN.
- Repeat with an incorrect password and a hidden network. Inspect the saved profile.
- Interrupt a transfer, reconnect, and confirm that no stale response becomes a new reply.
- Restart the agent and BlueZ, renew the device certificate, and reconnect.
- Exercise simultaneous clients and an adapter other than `hci0`.
- Prove a native phone client's GATT read, L2CAP connection, and TLS exchange with
  the deployed Wendy certificate chain before committing to a mobile release.

First-boot onboarding needs a separate, restricted setup protocol authenticated
by an out-of-band setup secret or device-key pin. It must establish ownership
before enabling Wi-Fi credential writes and enrollment. This recovery endpoint
never substitutes plaintext access for a missing certificate.
