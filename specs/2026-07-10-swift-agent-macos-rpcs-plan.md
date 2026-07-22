# Swift (macOS) Agent — Native gRPC Feature Implementations — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the macOS-feasible gRPC RPCs currently stubbed as `.unimplemented` in the Swift agent (hardware capabilities, Wi-Fi, audio, Bluetooth, resource/port stats, hostname, unprovision).

**Architecture:** Thin gRPC service adapters delegate to single-purpose macOS capability providers under `Sources/WendyAgent/Services/Platform/`. Stateless RPCs call providers directly; stateful streaming/session RPCs (Bluetooth, audio streaming) use actor-backed managers. Providers parse system-tool output via pure functions for hardware-free unit testing.

**Tech Stack:** Swift 6.2, grpc-swift-2, SwiftProtobuf, CoreWLAN, CoreAudio (AudioToolbox/CoreAudio HAL), AVFoundation, CoreBluetooth, IOKit, SystemConfiguration, Foundation.Process, Swift Testing.

## Global Constraints

- Target platform: macOS 15+; the agent runs in-process inside `WendyAgentMac` and inherits its TCC grants.
- The agent runs as the user (not root); privileged ops (`scutil --set`, some `networksetup` mutations) return honest errors, never silent success.
- Out of scope (keep existing `.unimplemented` messages): `runContainer`, `updateAgent`, `updateOS`, `getOSUpdateStatus`, `dumpKernelLog`, `attachContainer`, `listVolumes`, `removeVolume`, `streamMCP`, `createContainerWithProgress`, `queryChunks`, `writeChunks`, `queryLayers`, `listLayers`.
- Run `make format` (from `swift/`) before every commit.
- Providers must be injectable into services (protocol + default live impl) so RPC adapters unit-test against fakes.
- Update `Tests/WendyAgentTests/UnsupportedRPCTests.swift` to remove cases for RPCs that become implemented; keep cases for out-of-scope RPCs.
- Proto namespace prefix: `Wendy_Agent_Services_V1_`.

---

## File Structure

Created:
- `Sources/WendyAgent/Services/Platform/HardwareInventory.swift` — hardware discovery provider + parser.
- `Sources/WendyAgent/Services/Platform/WiFiController.swift` — CoreWLAN + networksetup provider.
- `Sources/WendyAgent/Services/Platform/AudioController.swift` — CoreAudio HAL provider + AVAudioEngine streaming.
- `Sources/WendyAgent/Services/Platform/BluetoothScanner.swift` — CoreBluetooth actor manager.
- `Sources/WendyAgent/Services/Platform/SystemStats.swift` — host + per-pid resource + listening-port helpers.
- `Sources/WendyAgent/Services/Platform/Subprocess.swift` — small shared `runProcess` helper (stdout/stderr/status), if not already extractable from ContainerService.
- Tests: `Tests/WendyAgentTests/HardwareInventoryTests.swift`, `WiFiControllerTests.swift`, `AudioControllerTests.swift`, `SystemStatsTests.swift`, `BluetoothScannerTests.swift`.

Modified:
- `Sources/WendyAgent/Services/AgentService.swift` — implement hardware caps, Wi-Fi (9), Bluetooth (4), setHostname; inject providers.
- `Sources/WendyAgent/Services/AudioService.swift` — implement 4 audio RPCs; inject provider.
- `Sources/WendyAgent/Services/ContainerService.swift` — implement getResourceStats, listContainerStats (real), getContainerPorts.
- `Sources/WendyAgent/Services/ProvisioningService.swift` — implement unprovision + persist provisioning state.
- `Sources/WendyAgent/WendyAgent.swift` — construct services with live providers.
- `Tests/WendyAgentTests/UnsupportedRPCTests.swift` — prune now-implemented cases.
- `WendyAgentMac/Support/WendyAgentMac-Info.plist` — add `NSLocationWhenInUseUsageDescription`.
- `WendyAgentMac/Sources/WelcomeAndPermissions.swift` — add `.location` permission case (CLLocationManager).
- `WendyAgentMac/Sources/WelcomeAndPermissionsView.swift` — render the new permission row.

---

## Task 1: Shared subprocess helper

**Files:**
- Create: `Sources/WendyAgent/Services/Platform/Subprocess.swift`
- Test: `Tests/WendyAgentTests/SystemStatsTests.swift` (shared file; first test lands here)

**Interfaces:**
- Produces: `enum Subprocess { static func run(_ executable: String, _ args: [String], timeout: Duration = .seconds(10)) async throws -> (status: Int32, stdout: String, stderr: String) }`

- [ ] **Step 1: Write failing test** — run `/bin/echo hello`, assert status 0 and stdout `hello`.

```swift
import Testing
@testable import WendyAgentCore

@Suite("Subprocess") struct SubprocessTests {
    @Test func echoesStdout() async throws {
        let r = try await Subprocess.run("/bin/echo", ["hello"])
        #expect(r.status == 0)
        #expect(r.stdout.trimmingCharacters(in: .whitespacesAndNewlines) == "hello")
    }
}
```

- [ ] **Step 2: Run** `swift test --filter SubprocessTests` — expect fail (no `Subprocess`).
- [ ] **Step 3: Implement** using the established `Task.detached` + `Foundation.Process` + `Pipe` pattern (model on `ContainerService.runBrewBundle`), capturing stdout/stderr to pipes with a byte cap (256 KiB) and a timeout that terminates then force-kills.
- [ ] **Step 4: Run** — expect pass.
- [ ] **Step 5: `make format` and commit** `feat(agent): add shared Subprocess helper for macOS providers`.

---

## Task 2: Hardware capabilities

**Files:**
- Create: `Sources/WendyAgent/Services/Platform/HardwareInventory.swift`
- Modify: `Sources/WendyAgent/Services/AgentService.swift`
- Modify: `Sources/WendyAgent/WendyAgent.swift`
- Test: `Tests/WendyAgentTests/HardwareInventoryTests.swift`

**Interfaces:**
- Produces: `protocol HardwareDiscovering: Sendable { func discover(categoryFilter: String?) async throws -> [HardwareCapability] }`
  - `struct HardwareCapability: Sendable, Equatable { let category, devicePath, description: String; let properties: [String:String] }`
  - `struct HardwareInventory: HardwareDiscovering` (live impl).
  - `static func parseSystemProfiler(displays: Data?, usb: Data?, camera: Data?, audio: Data?, storage: Data?) -> [HardwareCapability]` — pure, fixture-testable.
- Consumes (AgentService): injected `HardwareDiscovering` (default `HardwareInventory()`).

- [ ] **Step 1: Write failing test** — feed a captured `SPDisplaysDataType` JSON fixture to `parseSystemProfiler`, assert one `gpu` capability with expected name in `description`.

```swift
@Test func parsesGPUFromDisplaysJSON() {
    let json = Data(#"{"SPDisplaysDataType":[{"_name":"Apple M2","spdisplays_vendor":"Apple"}]}"#.utf8)
    let caps = HardwareInventory.parseSystemProfiler(displays: json, usb: nil, camera: nil, audio: nil, storage: nil)
    #expect(caps.contains { $0.category == "gpu" && $0.description.contains("Apple M2") })
}
```

- [ ] **Step 2: Run** `swift test --filter HardwareInventoryTests` — expect fail.
- [ ] **Step 3: Implement** `parseSystemProfiler` (decode each `system_profiler -json` top-level array, map salient keys into `properties`, set category/description/devicePath) and `discover` (invoke `Subprocess.run("/usr/sbin/system_profiler", ["-json","SPDisplaysDataType","SPUSBDataType", ...])` per type; add `network` from `getifaddrs`; filter by `categoryFilter`).
- [ ] **Step 4: Run** — expect pass.
- [ ] **Step 5:** In `AgentService`, add `let hardware: any HardwareDiscovering` with default; implement `listHardwareCapabilities` to call `hardware.discover(categoryFilter: request.message.hasCategoryFilter ? request.message.categoryFilter : nil)` and map to `Wendy_Agent_Services_V1_ListHardwareCapabilitiesResponse`. Update `WendyAgent.swift` registration if AgentService init signature changed.
- [ ] **Step 6: Add adapter test** in `HardwareInventoryTests` using a fake discoverer, asserting proto mapping. Run — expect pass.
- [ ] **Step 7:** Remove the `ListHardwareCapabilities` case from `UnsupportedRPCTests.swift`. Run `swift test --filter UnsupportedRPCTests` — expect pass.
- [ ] **Step 8: `make format` and commit** `feat(agent): implement listHardwareCapabilities on macOS`.

---

## Task 3: Host + container resource stats and ports

**Files:**
- Create: `Sources/WendyAgent/Services/Platform/SystemStats.swift`
- Modify: `Sources/WendyAgent/Services/ContainerService.swift`
- Test: `Tests/WendyAgentTests/SystemStatsTests.swift`

**Interfaces:**
- Produces:
  - `enum SystemStats { static func hostStats() -> HostSample; static func processStats(pid: Int32) -> ProcessSample?; static func listeningPorts(pid: Int32) async -> [PortSample] }`
  - `struct HostSample: Sendable { let cpuTotalTicks, cpuIdleTicks: UInt64; let cpuCount: UInt32; let memTotalBytes, memAvailableBytes: Int64 }`
  - `struct ProcessSample: Sendable { let cpuUsageNanos: UInt64; let memoryBytes: Int64 }`
  - `struct PortSample: Sendable, Equatable { let proto: String; let port: UInt32; let address: String }`
  - `static func parseLsofListen(_ output: String) -> [PortSample]` — pure, fixture-testable.

- [ ] **Step 1: Write failing test** for `parseLsofListen` with a captured `lsof -nP -iTCP -sTCP:LISTEN` sample; assert extracted `PortSample(proto:"tcp", port:8080, address:"0.0.0.0")`.
- [ ] **Step 2: Run** — expect fail.
- [ ] **Step 3: Implement** `parseLsofListen` (parse the `NAME` column `addr:port`, `NODE` column for proto), `hostStats` (`host_statistics64` for `vm_statistics64`, `sysctlbyname("hw.memsize")`, `host_processor_info`/`host_statistics` for CPU ticks, `activeProcessorCount`), `processStats` (`proc_pid_rusage(pid, RUSAGE_INFO_V2)` → `ri_user_time+ri_system_time` nanos, `ri_resident_size`), `listeningPorts` (`Subprocess.run("/usr/sbin/lsof", ["-nP","-iTCP","-sTCP:LISTEN","-a","-p","\(pid)"])` → `parseLsofListen`).
- [ ] **Step 4: Run** — expect pass.
- [ ] **Step 5:** In `ContainerService.getResourceStats`, build `HostStats` from `SystemStats.hostStats()` (map cpuTotal/idle ticks → jiffies fields, mem fields) and `containers` from each tracked app's `process?.processIdentifier` via `SystemStats.processStats`. In `listContainerStats`, populate `memoryBytes` from `processStats` (keep `appName`; `storageBytes` best-effort 0). In `getContainerPorts`, resolve the app by `request.message.appName`, read its pid, call `SystemStats.listeningPorts`, map to `PortEntry`.
- [ ] **Step 6:** Remove `GetResourceStats`, `GetContainerPorts` cases from `UnsupportedRPCTests.swift`. (listContainerStats was already implemented/empty — no unsupported case.) Run tests — expect pass.
- [ ] **Step 7: `make format` and commit** `feat(agent): implement resource stats and container ports on macOS`.

---

## Task 4: Provisioning unprovision + state

**Files:**
- Modify: `Sources/WendyAgent/Services/ProvisioningService.swift`
- Test: `Tests/WendyAgentTests/ProvisioningServiceTests.swift` (create)

**Interfaces:**
- `ProvisioningService` gains a state file URL (default under `WendyAgentPaths.stateDirectory/provisioning.json`), injectable for tests.
- `startProvisioning` writes state; `isProvisioned` reflects it; `unprovision` deletes it and returns success.

- [ ] **Step 1: Write failing test** — call `startProvisioning`, then `isProvisioned` returns provisioned; then `unprovision`; then `isProvisioned` returns not-provisioned. Use a temp state file.
- [ ] **Step 2: Run** `swift test --filter ProvisioningServiceTests` — expect fail.
- [ ] **Step 3: Implement** state persistence (write/read/delete a small JSON marker) and `unprovision`.
- [ ] **Step 4: Run** — expect pass.
- [ ] **Step 5:** Remove `Unprovision` case from `UnsupportedRPCTests.swift`. Run — expect pass.
- [ ] **Step 6: `make format` and commit** `feat(agent): implement provisioning state + unprovision on macOS`.

Note: keep the current `WendyAgent.swift` construction (`ProvisioningService()`); use default state path so no wiring change is needed beyond the optional init param.

---

## Task 5: setHostname

**Files:**
- Modify: `Sources/WendyAgent/Services/AgentService.swift`
- Test: `Tests/WendyAgentTests/AgentServiceHostnameTests.swift` (create)

**Interfaces:**
- Produces: `protocol HostnameSetting: Sendable { func setHostname(_ name: String) async throws }` with live impl `struct ScutilHostname` running `/usr/sbin/scutil --set HostName/LocalHostName/ComputerName`. Injected into `AgentService` (default live).
- Sanitize `LocalHostName` (RFC-952-ish: alnum + hyphen).

- [ ] **Step 1: Write failing test** — inject a fake `HostnameSetting` recording the name; call `setHostname`; assert recorded value and empty error in response. Add a second fake that throws → assert `RPCError` (permissionDenied) surfaces a clear message.
- [ ] **Step 2: Run** — expect fail.
- [ ] **Step 3: Implement** `ScutilHostname` (three `scutil` calls; non-zero status → throw with stderr) and `AgentService.setHostname` mapping success/failure.
- [ ] **Step 4: Run** — expect pass.
- [ ] **Step 5:** Remove `SetHostname` case from `UnsupportedRPCTests.swift`. Run — expect pass.
- [ ] **Step 6: `make format` and commit** `feat(agent): implement setHostname via scutil`.

---

## Task 6: Wi-Fi controller (read: status, scan, known)

**Files:**
- Create: `Sources/WendyAgent/Services/Platform/WiFiController.swift`
- Modify: `Sources/WendyAgent/Services/AgentService.swift`, `WendyAgent.swift`
- Test: `Tests/WendyAgentTests/WiFiControllerTests.swift`

**Interfaces:**
- Produces:
  - `protocol WiFiManaging: Sendable` with: `func status() async -> WiFiStatus`, `func scan() async throws -> [WiFiScanResult]`, `func knownNetworks() async -> [KnownWiFi]`, `func connect(ssid:String, password:String, security:WiFiSecurity?, hidden:Bool) async -> WiFiActionResult`, `func disconnect() async -> WiFiActionResult`, `func forget(ssid:String) async -> WiFiActionResult`, `func setPriority(ssid:String, priority:Int32) async -> WiFiActionResult`, `func reorder(ssids:[String]) async -> WiFiActionResult`.
  - Model structs `WiFiStatus`, `WiFiScanResult` (ssid, rssiDbm, signalStrength, security, isKnown, isConnected, priority), `KnownWiFi`, `WiFiActionResult { success: Bool; errorMessage: String? }`, `enum WiFiSecurity`.
  - `struct WiFiController: WiFiManaging` (live, CoreWLAN + networksetup).
  - `static func parsePreferredNetworks(_ output: String) -> [String]` — pure, fixture-testable (`networksetup -listpreferredwirelessnetworks`).
  - `static func rssiToSignalStrength(_ dbm: Int) -> Int32` — pure (-100..-30 → 0..100).
- Consumes (AgentService): injected `WiFiManaging` (default `WiFiController()`).

- [ ] **Step 1: Write failing tests** for `rssiToSignalStrength` (e.g. -50 → ~83, clamps -100→0, -30→100) and `parsePreferredNetworks` (fixture with header line + two SSIDs → `["Net1","Net2"]`).
- [ ] **Step 2: Run** — expect fail.
- [ ] **Step 3: Implement** the two pure helpers and the `WiFiController` live methods: obtain `CWWiFiClient.shared().interface()`; `status()` reads `.ssid()`/`.powerOn()`; `scan()` calls `scanForNetworks(withSSID: nil)`, dedups by SSID keeping strongest RSSI, marks isConnected/isKnown; `knownNetworks()` uses `networksetup -listpreferredwirelessnetworks <hardwarePort>` via `parsePreferredNetworks` (interface name from `interface.interfaceName`); mutating methods call `associate`/`disassociate`/`networksetup` and translate failures into `WiFiActionResult(success:false, errorMessage:)`.
- [ ] **Step 4: Run** — expect pass.
- [ ] **Step 5:** In `AgentService`, implement all 9 Wi-Fi RPCs as thin adapters mapping models ↔ proto (including `WiFiSecurity` ↔ `Wendy_Agent_Services_V1_WiFiSecurityType`). `getWiFiStatus`/`listWiFiNetworks` map provider models; mutation RPCs map `WiFiActionResult` to `success`/`errorMessage`.
- [ ] **Step 6: Adapter tests** with a fake `WiFiManaging` verifying proto mapping for status, list, connect-failure (populates errorMessage).
- [ ] **Step 7:** Remove all Wi-Fi cases from `UnsupportedRPCTests.swift`. Run — expect pass.
- [ ] **Step 8: `make format` and commit** `feat(agent): implement Wi-Fi RPCs via CoreWLAN + networksetup`.

---

## Task 7: Audio controller (list + set default)

**Files:**
- Create: `Sources/WendyAgent/Services/Platform/AudioController.swift`
- Modify: `Sources/WendyAgent/Services/AudioService.swift`, `WendyAgent.swift`
- Test: `Tests/WendyAgentTests/AudioControllerTests.swift`

**Interfaces:**
- Produces:
  - `protocol AudioManaging: Sendable { func listDevices(typeFilter: AudioKind?) async throws -> [AudioDeviceInfo]; func setDefault(deviceID: UInt32) async throws; func levels(deviceID: UInt32, rateHz: UInt32) -> AsyncThrowingStream<(peakDb:Float, rmsDb:Float), any Error>; func audio(deviceID: UInt32, sampleRate: UInt32, channels: UInt32) -> AsyncThrowingStream<(pcm:Data, sampleRate:UInt32, channels:UInt32), any Error> }`
  - `struct AudioDeviceInfo: Sendable, Equatable { let id: UInt32; let name: String; let kind: AudioKind; let isDefault: Bool }`
  - `enum AudioKind { case input, output }`
  - `struct AudioController: AudioManaging` (live; CoreAudio HAL for list/set, AVAudioEngine for streams).
- Consumes (AudioService): injected `AudioManaging` (default `AudioController()`).

- [ ] **Step 1: Write failing test** — inject a fake `AudioManaging` returning two devices; call `AudioService.listAudioDevices`; assert two `AudioDevice` messages with matching id/name/isDefault, and that `typeFilter` is forwarded.
- [ ] **Step 2: Run** — expect fail.
- [ ] **Step 3: Implement** `AudioController.listDevices` (query `kAudioHardwarePropertyDevices`; for each device read `kAudioDevicePropertyDeviceNameCFString`, determine input vs output via `kAudioDevicePropertyStreamConfiguration` channel counts, compare against default in/out device IDs) and `setDefault` (set `kAudioHardwarePropertyDefaultInputDevice`/`…DefaultOutputDevice` based on the device kind). Leave `levels`/`audio` implemented in Task 8; provide method stubs that the fake overrides for now (live stream bodies filled in Task 8).
- [ ] **Step 4: Run** — expect pass.
- [ ] **Step 5:** Implement `AudioService.listAudioDevices` + `setDefaultAudioDevice` as adapters mapping `AudioKind` ↔ `Wendy_Agent_Services_V1_AudioDeviceType`.
- [ ] **Step 6:** Remove `ListAudioDevices`, `SetDefaultAudioDevice` cases from `UnsupportedRPCTests.swift`. Run — expect pass.
- [ ] **Step 7: `make format` and commit** `feat(agent): implement audio device list + set default via CoreAudio`.

---

## Task 8: Audio streaming (levels + raw)

**Files:**
- Modify: `Sources/WendyAgent/Services/Platform/AudioController.swift`, `AudioService.swift`
- Test: `Tests/WendyAgentTests/AudioControllerTests.swift`

**Interfaces:** as defined in Task 7 (`levels`, `audio`).

- [ ] **Step 1: Write failing test** — `AudioService.streamAudioLevels` with a fake provider yielding two `(peakDb,rmsDb)` samples produces two `AudioLevelUpdate` messages with matching values.
- [ ] **Step 2: Run** — expect fail.
- [ ] **Step 3: Implement** the `AudioService.streamAudioLevels` + `streamAudio` adapters bridging the provider `AsyncThrowingStream` to `StreamingServerResponse` writers; implement live `AudioController.levels` (AVAudioEngine input tap computing peak/RMS dB per buffer at `rateHz`) and `audio` (tap emitting interleaved PCM `Data` at requested format via `AVAudioConverter` when needed).
- [ ] **Step 4: Run** — expect pass (adapter test uses fake; live tap covered by manual verification).
- [ ] **Step 5:** Remove `StreamAudioLevels`, `StreamAudio` cases from `UnsupportedRPCTests.swift`. Run — expect pass.
- [ ] **Step 6: `make format` and commit** `feat(agent): implement audio level + raw streaming via AVAudioEngine`.

---

## Task 9: Bluetooth scanner (scan + connect/disconnect; forget honest-unsupported)

**Files:**
- Create: `Sources/WendyAgent/Services/Platform/BluetoothScanner.swift`
- Modify: `Sources/WendyAgent/Services/AgentService.swift`, `WendyAgent.swift`
- Test: `Tests/WendyAgentTests/BluetoothScannerTests.swift`

**Interfaces:**
- Produces:
  - `protocol BluetoothManaging: Sendable { func scan() -> AsyncStream<DiscoveredPeripheral>; func connect(address: String) async -> BluetoothActionResult; func disconnect(address: String) async -> BluetoothActionResult }`
  - `struct DiscoveredPeripheral: Sendable, Equatable { let name: String; let address: String; let rssi: Int32; let deviceType: String; let paired: Bool; let connected: Bool; let trusted: Bool }`
  - `struct BluetoothActionResult { let success: Bool; let errorMessage: String? }`
  - `actor BluetoothScanner: BluetoothManaging` (live, `CBCentralManager`; `address` = `CBPeripheral.identifier.uuidString`).
- Consumes (AgentService): injected `BluetoothManaging` (default `BluetoothScanner()`).

- [ ] **Step 1: Write failing test** — inject a fake `BluetoothManaging` whose `scan()` yields two `DiscoveredPeripheral`; call `AgentService.scanBluetoothPeripherals`; collect the streamed responses; assert two `ScanBluetoothPeripheralsResponse` with matching discovered fields. Also assert `forgetBluetoothPeripheral` throws `.unimplemented` with a macOS-BLE explanation.
- [ ] **Step 2: Run** — expect fail.
- [ ] **Step 3: Implement** `BluetoothScanner` actor bridging `CBCentralManagerDelegate` callbacks into an `AsyncStream` (start scan on `scan()`, emit on `didDiscover`, map RSSI/name/identifier), and connect/disconnect resolving peripherals by identifier via `retrievePeripherals(withIdentifiers:)`.
- [ ] **Step 4: Run** — expect pass.
- [ ] **Step 5:** Implement `AgentService.scanBluetoothPeripherals` (stream adapter), `connectBluetoothPeripheral`, `disconnectBluetoothPeripheral` (map to `pair`/`trust` fields best-effort), and keep `forgetBluetoothPeripheral` as an honest `.unimplemented` with a message explaining CoreBluetooth is BLE-only and can't forget classic pairings.
- [ ] **Step 6:** Remove `ScanBluetoothPeripherals`, `ConnectBluetoothPeripheral`, `DisconnectBluetoothPeripheral` cases from `UnsupportedRPCTests.swift`; update the `ForgetBluetoothPeripheral` case's expected message to the new BLE-limitation wording. Run — expect pass.
- [ ] **Step 7: `make format` and commit** `feat(agent): implement Bluetooth scan/connect via CoreBluetooth`.

---

## Task 10: App-side Location permission for Wi-Fi

**Files:**
- Modify: `WendyAgentMac/Support/WendyAgentMac-Info.plist`
- Modify: `WendyAgentMac/Sources/WelcomeAndPermissions.swift`
- Modify: `WendyAgentMac/Sources/WelcomeAndPermissionsView.swift`

**Interfaces:** adds `Permission.location` and a `CLLocationManager`-backed status/request path mirroring the existing Bluetooth pattern.

- [ ] **Step 1:** Add `NSLocationWhenInUseUsageDescription` (string: "Wendy uses your location to scan and report nearby Wi-Fi networks.") to the Info.plist.
- [ ] **Step 2:** Add `case location` to `Permission` (title "Location", subtitle "Wi-Fi network scanning", systemImage "location"); implement `currentLocationStatus()` via `CLLocationManager.authorizationStatus`, `requestLocationAccess()` via `requestWhenInUseAuthorization()` + a `CLLocationManagerDelegate` continuation (mirror the `CBCentralManager` continuation pattern already in the file); include it in `shouldShowWelcomeAndPermissions`/`canFinish`/`status(for:)`/`requestPermission(_:)`/`refreshPermissionStatuses()`; add a Settings deep-link (`Privacy_LocationServices`).
- [ ] **Step 3:** Render the location row in `WelcomeAndPermissionsView` alongside the others.
- [ ] **Step 4:** Build the macOS app target (`make agent-start` or an `xcodebuild`/`swift build` of the app) to confirm it compiles.
- [ ] **Step 5: `make format` and commit** `feat(mac): request Location consent to enable Wi-Fi scanning`.

---

## Task 11: Wiring, full build, and verification

**Files:**
- Modify: `Sources/WendyAgent/WendyAgent.swift` (final review of service construction)

- [ ] **Step 1:** Confirm `WendyAgent.swift` constructs `AgentService`, `AudioService`, `ProvisioningService` with live providers (defaults are fine; explicit only where init changed).
- [ ] **Step 2:** `cd WendyAgentCore && swift build` — expect success.
- [ ] **Step 3:** `swift test` — expect all suites pass (including the pruned `UnsupportedRPCTests`).
- [ ] **Step 4: Manual verification** per `swift/AGENTS.md`: `make agent-start`; from `../go`: `go run ./cmd/wendy hardware`, `... device info`, `... wifi list`, `... wifi status`. Confirm non-empty, sane output (Wi-Fi requires Location granted). `make agent-stop`.
- [ ] **Step 5: `make format` and final commit** `chore(agent): finalize macOS RPC wiring`.

---

## Self-Review notes

- Spec coverage: hardware (T2), misc read-only stats/ports (T3), unprovision (T4), setHostname (T5), Wi-Fi 9 RPCs (T6), audio list/set (T7) + streaming (T8), Bluetooth (T9), Location app change (T10), wiring/verify (T11). All spec feature units mapped.
- Out-of-scope RPCs intentionally have no task and retain their `UnsupportedRPCTests` cases.
- Type consistency: provider protocol names (`HardwareDiscovering`, `WiFiManaging`, `AudioManaging`, `BluetoothManaging`, `HostnameSetting`) and model structs are referenced consistently across tasks and adapters.
