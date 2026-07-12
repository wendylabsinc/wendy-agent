# Swift macOS agent — cloud tunnel broker client

Date: 2026-07-11
Branch: `jo/swift-agent-macos-rpcs` (PR #1402)
Status: design approved, pending spec review

## Problem

A freshly-enrolled macOS device (Swift `WendyAgentCore` agent) does not "connect
to wendy cloud": it serves its local gRPC server + Bonjour + OTel, but never
dials out to the cloud, so it does not appear online in the dashboard and is not
remotely reachable by the `wendy` CLI over the cloud relay. The Go agent gets
this from its `TunnelBrokerClient`; the Swift agent has no equivalent.

## Goal

Port the Go agent's `TunnelBrokerClient` (`go/internal/agent/services/tunnel_broker_client.go`)
to the Swift agent so that, while provisioned, the Mac:

1. Dials the cloud tunnel broker and registers presence (→ shows online).
2. Serves broker-initiated dial requests by relaying CLI↔device byte streams to
   its own local mTLS port, so `wendy` can reach the device through the cloud.

End-to-end mTLS terminates at the agent's real mTLS server (port + 1); the broker
and the tunnel client's outer TLS only carry ciphertext.

## Non-goals

- **Linux mesh data-plane** (VIP interception, iptables/CNI/`SO_ORIGINAL_DST`,
  per-app bridge DNS, cross-device VIP routing). It depends on Linux-kernel
  primitives that do not exist on macOS and sits behind a VM boundary the host
  agent cannot netfilter; a macOS mesh would be a new subsystem, not a port.
  Deferred to a separate research spike.
- The CLI-side `ClientTunnel` RPC (already implemented in the Go CLI).
- Cloud telemetry flushing (separate concern, not part of "reachability").

## Reference contract (Go, authoritative)

- Proto: `Proto/cloud/tunnel.proto`, `service TunnelBrokerService`, package
  `wendycloud.v1`.
  - `RegisterPresence(stream AgentHeartbeat) returns (stream DialRequest)`
  - `AgentTunnel(stream TunnelData) returns (stream TunnelData)`
  - (`ClientTunnel` is CLI-only; not used by the agent.)
- Auth to broker: **server-auth-only TLS, no client certificate** (the broker
  uses `NoClientCert` because Go's TLS stack rejects the ML-DSA client certs
  pki-core issues). The broker's server cert is validated against the device's
  own CA chain (`chainPEM`); **hostname verification is skipped** (broker cert CN
  is `localhost`). Identity is asserted at the application layer via metadata
  headers `x-wendy-client-cert` and `x-forwarded-client-cert`, both set to
  `URI=urn:wendy:org:<orgID>:asset:<assetID>`.
- Session lifecycle: broker pushes `DialRequest{session_id, host, port}` on the
  presence stream; the agent dials the local service, opens an `AgentTunnel`
  stream, sends a first `TunnelData{session_id}` (empty payload) to claim the
  session, then relays payload bytes both directions. `half_close` on a message
  closes the write half of the paired side.
- Heartbeat: `AgentHeartbeat` every 30s on the presence stream.
- Reconnect: exponential backoff, `1s * 2^attempt` capped at 90s; reset on a
  clean session.
- SSRF guard: a `DialRequest` is served only if `host` is `localhost` or a
  loopback IP; otherwise dropped.
- Port remap: the CLI always requests the well-known mTLS port 50052; if the
  agent's actual mTLS port differs, a request for 50052 is remapped to it.
- Broker URL: `WENDY_BROKER_URL` if set, else derived from the provisioning
  `cloudHost` — if it ends in `:443` use it verbatim, else `<host>:50052`.

## Architecture

One new well-bounded unit plus lifecycle wiring in `WendyAgent`.

### `TunnelBrokerClient` (`Sources/WendyAgent/Cloud/TunnelBrokerClient.swift`)

A `Sendable` value type mirroring `CloudCertificateClient`'s injectable-seam
pattern so tests need no network.

```
struct TunnelBrokerClient: Sendable {
    struct Config: Sendable {
        var brokerURL: String
        var orgID: Int32
        var assetID: Int32
        var chainPEM: String
        var mtlsPort: Int
    }

    // The seam: run ONE presence session end-to-end — dial the broker, register
    // presence, heartbeat, and serve/relay every DialRequest until the session
    // ends (clean disconnect or error, which it throws). `.live` performs the
    // real gRPC dial + relay; tests inject a fake that drives dial requests
    // against a fake local server.
    var runSession: @Sendable (_ config: Config) async throws -> Void

    static let live: TunnelBrokerClient

    // Backoff/reconnect loop around `runSession`; cancellation-aware. This is the
    // entry point WendyAgent starts.
    func runForever(config: Config) async { /* loop: runSession; backoff; until cancelled */ }
}
```

`.live` owns the whole session:

- **Dial** the broker with `HTTP2ClientTransport.Posix`, `TransportSecurity.tls`
  configured with `trustRoots = .certificates([chainPEM])` and a
  `customVerificationCallback` that validates the peer chain to those roots and
  **ignores hostname** (mirrors Go's `VerifyConnection`; reuses the same
  swift-certificates `Verifier` + `RFC5280Policy` approach as
  `ClientCertAuthorizer`). No client certificate is presented.
- Attach identity metadata (`x-wendy-client-cert`, `x-forwarded-client-cert`).
- Open `RegisterPresence`; spawn a heartbeat task (30s `AgentHeartbeat`); iterate
  received `DialRequest`s, spawning `handleDial` per request in a task group so
  concurrent sessions are independent.
- `handleDial(_:)`: SSRF-guard host → loopback only; remap port; open a plain
  TCP connection to `127.0.0.1:<port>` via NIO (`ClientBootstrap` /
  `NIOAsyncChannel<ByteBuffer, ByteBuffer>`); open `AgentTunnel`; send join
  `TunnelData{sessionId}`; then call `relay(...)`.

**Relay** is factored into its own internal function, generic over an abstract
duplex message channel (a small protocol the `AgentTunnel` stream conforms to)
plus the `NIOAsyncChannel`, so it can be unit-tested with in-memory fakes
independent of the live gRPC dial. It runs two concurrent directions in a task
group: gRPC→TCP (write inbound payloads to the socket; on `half_close`, close the
socket write half) and TCP→gRPC (read socket, send `TunnelData{payload}`; on EOF
send `TunnelData{half_close: true}` then finish). Either side finishing tears
down the session.

Backoff state (`backoff(attempt:)`) is a pure function, unit-tested separately.

### Pure helpers (testable without network)

- `TunnelBrokerClient.brokerURL(cloudHost:override:)` → mirrors
  `brokerURLForCloudHost` / `clouddefaults.BrokerURL`.
- `TunnelBrokerClient.identityHeaderValue(orgID:assetID:)` → the
  `URI=urn:wendy:org:<org>:asset:<asset>` string.
- `TunnelBrokerClient.isLoopback(host:)` → SSRF guard.
- `TunnelBrokerClient.remapPort(requested:mtlsPort:)` → 50052→actual remap.
- `TunnelBrokerClient.backoff(attempt:)` → `min(1s*2^attempt, 90s)`.

### Proto generation

Add `"$PROTO_DIR/cloud/tunnel.proto"` to the WendyCloud section of
`swift/Scripts/GenerateProto.sh` and regenerate. This produces
`tunnel.pb.swift` + `tunnel.grpc.swift` in
`Sources/WendyAgentCore/Sources/WendyCloudGRPC/Proto/cloud/`. The generator is
the version-pinned `generate-grpc-code-from-protos` SPM plugin, so it regenerates
deterministically and does not churn the other cloud files. The agent uses only
the `RegisterPresence` and `AgentTunnel` client methods.

**Fallback:** if the plugin cannot run under the current swift-crypto/macOS-27
build blocker, hand-write the four small message types (`AgentHeartbeat`,
`DialRequest`, `TunnelData`, and the `TunnelBrokerService` client shim for the
two agent RPCs) as a stopgap, to be replaced by generated code in CI.

### Lifecycle wiring in `WendyAgent`

Add `private var tunnelBrokerTask: Task<Void, Never>?`, tied to the **mTLS main
server's lifetime**:

- In `startMainServer`, after the server is listening, when `isMTLS` and certs
  are present and the device org is known: build the `Config` (broker URL from
  provisioning `cloudHost` + `WENDY_BROKER_URL`; orgID/assetID from provisioning
  info; chainPEM from provisioning certs; `mtlsPort = configuration.port + 1`)
  and start `tunnelBrokerTask = Task { await client.runForever(config:) }`.
- In `stopMainServer`, cancel and await `tunnelBrokerTask`, then nil it.

Because `switchMainServer` calls `stopMainServer`→`startMainServer`, this yields
the correct behavior with no new state machine:

| Transition | stopMainServer | startMainServer |
|---|---|---|
| provision (plaintext→mTLS) | cancels nothing (not running) | starts broker |
| unprovision (mTLS→plaintext) | cancels broker | plaintext, no broker |
| full `stop()` | cancels broker | — |

`clearRuntimeState()` also nils `tunnelBrokerTask` (defensive, matching the other
task handles).

## Data flow

```
CLI  --mTLS-->  cloud broker  --DialRequest-->  agent (RegisterPresence)
                                                   |
                                          dial 127.0.0.1:50052 (own mTLS server)
                                                   |
CLI  <==== opaque bytes relayed via AgentTunnel <==>  local TCP  <-> mTLS server
```

The broker never sees plaintext: the CLI's mTLS session runs end-to-end to the
agent's real mTLS server, which the relay simply pipes bytes to/from.

## Error handling

- Cloud outage / broker unreachable: the reconnect loop retries with backoff in
  the background; local operation (mDNS, LAN mTLS) is unaffected. Never fatal.
- Missing `chainPEM` or undeterminable device org: log a warning and do not start
  the broker (matches Go), rather than dialing with no trust anchor.
- Per-session failures (local dial fails, stream error): log and drop that
  session; the presence stream stays up.
- Task cancellation (server switch / stop): cooperative — the loop and any active
  relays observe cancellation and unwind; the local TCP connection and the
  `AgentTunnel` stream are closed.

## Security

- Server-auth-only outer TLS with the device CA chain as trust anchor + hostname
  skipped is intentional and matches the Go contract; end-to-end mTLS still
  protects the actual RPC traffic.
- SSRF guard restricts dial targets to loopback, so a malicious/compromised
  broker cannot direct the agent to dial arbitrary hosts.
- The identity header is derived from the device's own provisioning identity
  (org + asset), not attacker-controlled input.

## Testing

Build/test remains **CI-deferred** on this box (the swift-crypto vs macOS-27 SDK
`ContiguousBytes` blocker fails the dependency before agent code compiles). Tests
are written as the spec and run in CI:

- Unit (no network, via the injectable seam and pure helpers):
  - `brokerURL` derivation (`:443` verbatim, else `:50052`, `WENDY_BROKER_URL`
    override).
  - identity header formatting.
  - loopback SSRF guard (accepts `localhost`/127.0.0.1/::1; rejects public IPs
    and names).
  - port remap (50052→mtlsPort when different; left alone otherwise).
  - backoff schedule (1s,2s,4s,…,capped 90s).
- Relay: with an in-memory fake `AgentTunnel` stream + a local echo TCP server,
  assert bidirectional byte relay and half-close semantics.
- CI-deferred E2E: a real provisioned device registering with a live broker and
  a `wendy` CLI reaching it through the cloud — hardware/CI-gated, consistent
  with the rest of PR #1402.

## Files

- New: `Sources/WendyAgent/Cloud/TunnelBrokerClient.swift`
- New: `Tests/WendyAgentTests/TunnelBrokerClientTests.swift`
- Modified: `swift/Scripts/GenerateProto.sh` (+ generated `tunnel.pb.swift`,
  `tunnel.grpc.swift`)
- Modified: `Sources/WendyAgent/WendyAgent.swift` (tunnelBrokerTask lifecycle in
  `startMainServer`/`stopMainServer`/`clearRuntimeState`)
