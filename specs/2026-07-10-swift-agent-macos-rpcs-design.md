# Swift (macOS) Agent — Native gRPC Feature Implementations

Date: 2026-07-10
Branch: `jo/swift-agent-macos-rpcs`

## Problem

The Swift macOS agent (`WendyAgentCore`) implements the native app lifecycle
(brew/native process management) but leaves a large set of gRPC RPCs throwing
`RPCError(code: .unimplemented)`. Many of these are genuinely feasible on macOS
using native system frameworks, and their absence means `wendy` CLI commands
(`device info`, `hardware`, `wifi …`, audio/bluetooth tooling) do not work when
the target device is a Mac.

This spec covers implementing the **macOS-feasible** RPCs. Genuinely
Linux/WendyOS-specific RPCs (OCI layer/chunk transfer, OTA `updateOS` /
`getOSUpdateStatus`, `updateAgent`, `dumpKernelLog`, legacy container streaming)
are **explicitly out of scope** and remain `.unimplemented` with their existing
explanatory messages.

## Key architectural facts

- **The agent runs in-process** inside the `WendyAgentMac` menu-bar app
  (`AppDelegate` holds `WendyAgent(configuration: .default)` and starts it in a
  `Task`). Core services therefore can use CoreWLAN / CoreBluetooth / CoreAudio /
  IOKit / SystemConfiguration directly and **inherit the app process's TCC
  permission grants**.
- The app already requests **Bluetooth + microphone + camera** consent
  (`WelcomeAndPermissions`). It does **not** request **Location**, which
  macOS 14+ requires before CoreWLAN returns real SSIDs and scan results.
- The app runs **as the user, not root**. `scutil --set` (hostname) and some
  `networksetup` mutations (forget / priority / reorder preferred networks) may
  fail without elevation.
- gRPC services are registered in `WendyAgent.swift` (~line 210):
  `AgentService`, `ContainerService`, `AudioService`, `ProvisioningService`,
  `TelemetryService`, `FileSyncService`.
- The proto messages are already generated and map cleanly to macOS
  (`AudioDevice.id: UInt32` ↔ CoreAudio `AudioDeviceID`; `WiFiNetwork` has
  ssid / signalStrength / rssiDbm / security / isKnown / isConnected / priority;
  `HardwareCapability` = category / devicePath / description / properties;
  `DiscoveredBluetoothPeripheral` = name / address / rssi / deviceType / paired /
  connected / trusted).

## Design principles

- **Thin RPC adapters over single-purpose providers.** Introduce macOS
  capability providers under `Sources/WendyAgent/Services/Platform/`, each
  wrapping exactly one system framework and independently testable. The gRPC
  service methods become thin: decode request → call provider → map to proto.
  This mirrors the Go agent's `hardwareDiscoverer` / manager-injection style and
  keeps framework code out of the RPC layer.
- **Stateless vs stateful.** Stateless request/response RPCs stay as `struct`
  service methods calling a provider. Stateful sessions (Bluetooth scan/connect,
  audio streaming) get **actor-backed managers** injected into the service.
- **Honest failure.** Where macOS blocks an operation (missing Location, missing
  privilege, BLE-only limitation), return a specific error — either
  `RPCError(code: .permissionDenied/.unimplemented, message:)` or the response's
  `success=false` + `errorMessage`, matching each RPC's proto contract — rather
  than pretending success.
- **Testability without hardware.** Providers parse captured tool output
  (`system_profiler -json`, `networksetup`) via pure functions unit-tested
  against fixtures. RPC adapters are tested against fake providers. No live
  radios in CI.

## Feature units

Each unit is an independent, testable slice. They are delivered together in one
change set (per user direction) but organized as separable commits.

### 1. Hardware capabilities — `AgentService.listHardwareCapabilities`

Provider: `HardwareInventory`.

- Source data from `system_profiler -json` data types and `getifaddrs`:
  - `SPDisplaysDataType` → `gpu`
  - `SPUSBDataType` → `usb`
  - `SPCameraDataType` → `camera`
  - `SPAudioDataType` → `audio`
  - `SPStorageDataType` / `SPNVMeDataType` → `storage`
  - `getifaddrs` / SystemConfiguration → `network`
- Honors `categoryFilter` (single-category filter, matching the Go/CLI contract).
- Omits Linux-only `i2c` / `spi` / `gpio` (no macOS equivalent).
- Each capability: `category`, `devicePath` (best-effort, e.g. IORegistry path or
  BSD name), `description`, `properties` (string map of salient attributes).

### 2. Misc read-only stats + provisioning

- `ContainerService.getResourceStats` — system CPU / memory / load via
  `host_statistics64` + `sysctl` (`hw.memsize`, `vm.loadavg`) mapped to the
  `GetResourceStatsResponse` proto.
- `ContainerService.listContainerStats` — replace today's empty-stat stub with
  real per-app CPU% and RSS derived from each tracked native app's PID
  (`proc_pid_rusage` / `task_info`). Apps with no live process report zeros.
- `ContainerService.getContainerPorts` — report the TCP ports the tracked
  native app process is listening on (via `lsof -nP -iTCP -sTCP:LISTEN -p <pid>`
  or `proc_pidfdinfo`), mapped to `GetContainerPortsResponse`.
- `ProvisioningService.unprovision` — clear the local provisioning state written
  by `startProvisioning`, returning success. (Currently `startProvisioning`
  no-ops and `isProvisioned` reports not-provisioned, so `unprovision` becomes a
  clean idempotent state reset.)

No new permissions required.

### 3. Wi-Fi — `AgentService` (9 RPCs)

Provider: `WiFiController` over CoreWLAN (`CWWiFiClient.shared()`,
`CWInterface`).

- `getWiFiStatus` — current interface power state, connected SSID (via
  `interface.ssid()`), populate `connected` / `ssid`.
- `listWiFiNetworks` — `interface.scanForNetworks(withSSID:)`; map each
  `CWNetwork` to `WiFiNetwork` (ssid, rssiDbm from `.rssiValue`, `signalStrength`
  normalized 0–100, `security` from `CWSecurity`, `isConnected` vs current SSID,
  `isKnown` vs saved profiles).
- `listKnownWiFiNetworks` — saved/preferred networks via
  `networksetup -listpreferredwirelessnetworks <iface>` (+ `configuration`
  profiles); map to `KnownWiFiNetwork` (ssid, uuid best-effort, priority = order
  index, security best-effort).
- `connectToWiFi` — `interface.associate(to:password:)` (user-level OK for
  open/WPA). Honor `hidden` / `security` hints where CoreWLAN allows. Populate
  `success` / `errorMessage`.
- `disconnectWiFi` — `interface.disassociate()`.
- `forgetWiFiNetwork` / `setWiFiNetworkPriority` / `reorderKnownWiFiNetworks` —
  `networksetup -removepreferredwirelessnetwork` / reorder preferred-network
  list. These modify system network config and may require admin; on failure
  return `success=false` + a clear `errorMessage`.

**App change:** Add CoreLocation consent to `WelcomeAndPermissions` (a
`.location` permission case using `CLLocationManager.requestWhenInUseAuthorization`)
and `NSLocationWhenInUseUsageDescription` to the app Info.plist, so scan/status
return real SSIDs. Without Location, scan/status degrade to empty results with a
diagnostic `errorMessage` rather than crashing.

### 4. Audio — `AudioService` (4 RPCs)

Provider: `AudioController` over CoreAudio HAL + `AVAudioEngine`.

- `listAudioDevices` — enumerate devices via
  `kAudioHardwarePropertyDevices`; classify input/output; honor `typeFilter`;
  populate `AudioDevice` (id = `AudioDeviceID`, name, type, isDefault).
- `setDefaultAudioDevice` — set
  `kAudioHardwarePropertyDefaultInputDevice` / `…DefaultOutputDevice`.
- `streamAudioLevels` — actor-driven `AsyncStream` computing peak/RMS dB from an
  `AVAudioEngine` input tap at the requested `updateRateHz`.
- `streamAudio` — stream raw PCM `AudioChunk`s at the requested
  `sampleRate` / `channels` from the input tap. Uses the existing microphone
  entitlement.

### 5. Bluetooth — `AgentService` (4 RPCs)

Provider: `BluetoothScanner` actor over CoreBluetooth (`CBCentralManager`, uses
existing BT consent).

- `scanBluetoothPeripherals` — start a scan; stream `DiscoveredBluetoothPeripheral`
  as peripherals are discovered (name, address = peripheral `identifier` UUID,
  rssi, deviceType best-effort from advertisement, connected state).
- `connectBluetoothPeripheral` / `disconnectBluetoothPeripheral` — connect/cancel
  by identifier for BLE peripherals.
- `forgetBluetoothPeripheral` — **honest limitation:** CoreBluetooth is BLE-only
  and does not expose classic pairing/forget the way BlueZ does; returns a clear
  `.unimplemented`/error explaining the macOS limitation. BLE scan/connect/
  disconnect work.

Note: `address` in the proto is a BlueZ MAC on Linux; on macOS CoreBluetooth
hides MACs and exposes a per-host `CBPeripheral.identifier` UUID. We use that
UUID string as the stable `address`, and connect/disconnect resolve peripherals
by it. This is documented as the macOS addressing convention.

### 6. `setHostname` — `AgentService.setHostname`

`scutil --set HostName` / `--set LocalHostName` / `--set ComputerName`. Requires
root; on failure return a clear permission error.

## Out of scope (remain `.unimplemented`)

`runContainer` (streaming + legacy layers), `updateAgent`, `updateOS`,
`getOSUpdateStatus`, `dumpKernelLog`, `attachContainer`, `listVolumes` /
`removeVolume` (Linux app-volume semantics), `streamMCP`,
`createContainerWithProgress`, `queryChunks` / `writeChunks` / `queryLayers` /
`listLayers`. Each keeps its existing explanatory message.

## Testing & verification

- **Unit:** Provider parsers (`system_profiler` JSON, `networksetup` output,
  CoreAudio device lists) are pure functions tested against captured fixtures.
  RPC adapters tested against fake providers via Swift Testing.
- **Integration / manual:** Build + `make agent-start`, then drive the Go CLI
  (`cd ../go && go run ./cmd/wendy …`) against the local agent:
  `wendy hardware`, `wendy device info`, `wendy wifi list/status`,
  audio/bluetooth tooling. Stop with `make agent-stop`.
- **Formatting:** `make format` before every commit (per `swift/AGENTS.md`).

## Risks

- CoreWLAN scan/status silently returns empty without Location authorization; the
  app-side consent addition is required for full Wi-Fi functionality, and the
  agent is a background/menu-bar process so first-run consent UX matters.
- Privileged `networksetup` / `scutil` mutations fail for a non-root agent;
  surfaced as honest errors, not silent success.
- CoreBluetooth BLE-only model diverges from the Linux BlueZ semantics the proto
  was designed around (MAC vs UUID, no classic forget).
