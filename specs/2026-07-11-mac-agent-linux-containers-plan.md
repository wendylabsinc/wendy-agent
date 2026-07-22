# Linux Containers on the Mac Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Wendy Agent for Mac run Linux/arm64 container apps via Apple's `container` runtime (primary) and Docker (secondary), delivered through an in-process OCI registry the agent hosts on `localhost:5555`.

**Architecture:** A `LinuxContainerBackend` protocol abstracts two subprocess backends (`ContainerCLIBackend` over `container`, `DockerContainerBackend` over `docker`). Entitlements are interpreted once into a neutral `[LinuxRunSpec]` and each backend renders it to its own CLI flags. An embedded Hummingbird OCI Distribution v2 server, backed by the agent's existing on-disk blob store, receives the CLI's registry push and serves pulls to the runtime. `ContainerService` gains a real Linux create/start/stop/remove path that reuses the existing process-streaming machinery; the CLI's darwin-linux block is lifted.

**Tech Stack:** Swift 6.2 (strict concurrency, `.v6` language mode), grpc-swift-2, Hummingbird 2 (new), Foundation.Process, Swift Testing (`@Test`/`#expect`). Go (CLI) with standard `testing`.

## Global Constraints

- Swift package requires Swift 6 language mode; all new types must satisfy strict-concurrency (`Sendable` where crossing isolation). Copy the pattern from `DockerCLI` (a `Sendable` struct) and actor backends.
- New Swift sources live under `swift/WendyAgentCore/Sources/WendyAgent/`; tests under `swift/WendyAgentCore/Tests/WendyAgentTests/`. **The module name is `WendyAgentCore`** (the SwiftPM target name), even though sources sit in `Sources/WendyAgent` — tests must use `@testable import WendyAgentCore`.
- Config types are fixed (in `Services/OCITypes.swift`; do not modify them). Exact initializers: `WendyAppConfig(appId: String, platform: String?, entitlements: [WendyEntitlement]?, brewfile: String? = nil)` — **`appId` is required and first**; `WendyEntitlement(type: String, mode: String?, name: String?, path: String?, ports: [WendyPortMapping]?)`; `WendyPortMapping(host: UInt16, container: UInt16)`. Every `WendyAppConfig(...)` in a test must pass `appId:`.
- Registry port is `5555` (matches Go `registryPort("darwin")` in `helpers.go:2513` and `DockerCLI.registryPort`). Bind to `127.0.0.1` only.
- Container naming: managed containers are named `wendy-<appName>` and carry label `wendy.managed=true` (matches the existing Docker backend and Go provider).
- Runtime selection order: prefer `container`, else `docker`, else neither (Linux apps unsupported with an actionable message).
- Use Swift Testing (`import Testing`), not XCTest, for new tests (matches the repo's existing test suite).
- Run `swift/Scripts/Test.sh` (or `swift test` in `swift/WendyAgentCore`) for Swift; `go test ./...` from repo root for Go.
- swift-format all new/changed Swift before the final commit (`swift/Scripts/Lint.sh` or the project `.swift-format`).

---

### Task 1: `LinuxContainerBackend` protocol + entitlement interpretation

Introduces the backend abstraction and the single source of truth for turning entitlements into a runtime-neutral run spec. Pure logic — no subprocess, fully unit-testable.

**Files:**
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Containers/LinuxContainerBackend.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/LinuxRunSpecTests.swift`

**Interfaces:**
- Consumes: `WendyAppConfig`, `WendyEntitlement`, `WendyPortMapping` (from `Services/OCITypes.swift`).
- Produces:
  - `enum LinuxRunSpec: Equatable, Sendable { case networkNone; case publishPort(host: UInt16, container: UInt16); case volume(name: String, path: String) }`
  - `struct LinuxContainerInfo: Sendable, Equatable { let id: String; let name: String; let state: String }`
  - `protocol LinuxContainerBackend: Sendable { func pull(image: String) async throws; func createAndStart(appName: String, imageName: String, appConfig: WendyAppConfig?, terminationHandler: (@Sendable (Foundation.Process) -> Void)?) async throws -> (process: Foundation.Process, stdout: Pipe, stderr: Pipe); func stop(appName: String) async throws; func remove(appName: String) async throws; func listContainers() async throws -> [LinuxContainerInfo] }`
  - `enum LinuxRunSpecBuilder { static func specs(from entitlements: [WendyEntitlement], appName: String, warn: (String) -> Void) -> [LinuxRunSpec] }`
  - `func managedContainerName(for appName: String) -> String` returning `"wendy-\(appName)"`.

- [ ] **Step 1: Write the failing test**

```swift
// swift/WendyAgentCore/Tests/WendyAgentTests/LinuxRunSpecTests.swift
import Testing

@testable import WendyAgentCore

@Suite struct LinuxRunSpecTests {
    @Test func mapsNetworkNone() {
        let ents = [WendyEntitlement(type: "network", mode: "none", name: nil, path: nil, ports: nil)]
        let specs = LinuxRunSpecBuilder.specs(from: ents, appName: "app", warn: { _ in })
        #expect(specs == [.networkNone])
    }

    @Test func mapsPublishedPorts() {
        let ents = [
            WendyEntitlement(
                type: "network", mode: nil, name: nil, path: nil,
                ports: [WendyPortMapping(host: 8080, container: 80)]
            )
        ]
        let specs = LinuxRunSpecBuilder.specs(from: ents, appName: "app", warn: { _ in })
        #expect(specs == [.publishPort(host: 8080, container: 80)])
    }

    @Test func mapsPersistVolumeWithNamespacedName() {
        let ents = [WendyEntitlement(type: "persist", mode: nil, name: "data", path: "/var/data", ports: nil)]
        let specs = LinuxRunSpecBuilder.specs(from: ents, appName: "app", warn: { _ in })
        #expect(specs == [.volume(name: "wendy-app-data", path: "/var/data")])
    }

    @Test func warnsOnHardwareEntitlementAndEmitsNoSpec() {
        var warnings: [String] = []
        let ents = [WendyEntitlement(type: "gpu", mode: nil, name: nil, path: nil, ports: nil)]
        let specs = LinuxRunSpecBuilder.specs(from: ents, appName: "app", warn: { warnings.append($0) })
        #expect(specs.isEmpty)
        #expect(warnings.count == 1)
        #expect(warnings[0].contains("gpu"))
    }

    @Test func managedNameIsPrefixed() {
        #expect(managedContainerName(for: "myapp") == "wendy-myapp")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter LinuxRunSpecTests`
Expected: FAIL — `LinuxRunSpecBuilder` / `managedContainerName` not defined.

- [ ] **Step 3: Write minimal implementation**

```swift
// swift/WendyAgentCore/Sources/WendyAgent/Containers/LinuxContainerBackend.swift
import Foundation

/// Runtime-neutral description of one container run flag, interpreted from a
/// Wendy entitlement. Each concrete backend renders these to its own CLI flags.
enum LinuxRunSpec: Equatable, Sendable {
    case networkNone
    case publishPort(host: UInt16, container: UInt16)
    case volume(name: String, path: String)
}

/// A Wendy-managed container as reported by a runtime's list command.
struct LinuxContainerInfo: Sendable, Equatable {
    let id: String
    let name: String
    let state: String
}

/// The managed container name for an app (`wendy-<appName>`).
func managedContainerName(for appName: String) -> String { "wendy-\(appName)" }

/// A Linux-container runtime the Mac agent can drive (Apple `container` or Docker).
protocol LinuxContainerBackend: Sendable {
    func pull(image: String) async throws
    func createAndStart(
        appName: String,
        imageName: String,
        appConfig: WendyAppConfig?,
        terminationHandler: (@Sendable (Foundation.Process) -> Void)?
    ) async throws -> (process: Foundation.Process, stdout: Pipe, stderr: Pipe)
    func stop(appName: String) async throws
    func remove(appName: String) async throws
    func listContainers() async throws -> [LinuxContainerInfo]
}

/// Interprets entitlements into runtime-neutral run specs. Single source of
/// truth shared by every backend so mapping stays consistent.
enum LinuxRunSpecBuilder {
    /// Hardware entitlements that VM-isolated Linux containers can't honor on macOS.
    static let unsupportedHardwareTypes: Set<String> = [
        "gpu", "bluetooth", "audio", "video", "camera", "usb", "i2c", "gpio",
    ]

    static func specs(
        from entitlements: [WendyEntitlement],
        appName: String,
        warn: (String) -> Void
    ) -> [LinuxRunSpec] {
        var specs: [LinuxRunSpec] = []
        for entitlement in entitlements {
            switch entitlement.type {
            case "network":
                if entitlement.mode == "none" {
                    specs.append(.networkNone)
                } else if let ports = entitlement.ports {
                    for port in ports {
                        specs.append(.publishPort(host: port.host, container: port.container))
                    }
                }
            case "persist":
                if let name = entitlement.name, let path = entitlement.path {
                    specs.append(.volume(name: "wendy-\(appName)-\(name)", path: path))
                }
            case let type where unsupportedHardwareTypes.contains(type):
                warn(
                    "Entitlement '\(type)' is not available for Linux containers on macOS (VM isolation)"
                )
            default:
                warn("Unknown entitlement type '\(entitlement.type)'")
            }
        }
        return specs
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd swift/WendyAgentCore && swift test --filter LinuxRunSpecTests`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Containers/LinuxContainerBackend.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/LinuxRunSpecTests.swift
git commit -m "feat(mac): LinuxContainerBackend protocol + entitlement run-spec mapping"
```

---

### Task 2: Shared `ExecutableResolver` + `ContainerCLI` over Apple's `container`

A `Sendable` `ContainerCLI` struct: pure argument builders, an availability probe, and `pull`/`runAttached`/`stop`/`delete`/`list`. To avoid duplicating process plumbing, extract a shared `ExecutableResolver` (PATH + homebrew lookup) used by both `DockerCLI` and `ContainerCLI`, and route `ContainerCLI`'s short commands through the existing `Subprocess.run`. Only the small attached-run plumbing stays per-CLI. Argument construction is unit-tested without a live runtime via pure arg-builder functions.

**Files:**
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Containers/ExecutableResolver.swift`
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Containers/ContainerCLI.swift`
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Docker/DockerCLI.swift:331-394` (replace its private `resolveExecutablePath`/`resolveExecutable`/`buildSearchPaths`/`fallbackExecutablePaths` with calls to the shared `ExecutableResolver`; keep `resolveExecutableForTesting`)
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/ExecutableResolverTests.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/ContainerCLITests.swift`

**Interfaces:**
- Consumes: `LinuxRunSpec` (Task 1), `LinuxContainerInfo` (Task 1), existing `Subprocess.run(_:_:timeout:) -> Subprocess.Result` (`Services/Platform/Subprocess.swift`).
- Produces:
  - `enum ExecutableResolver { struct Resolution: Sendable { let resolvedPath: String?; let searchedPaths: [String] }; static func resolve(_ executable: String, environment: [String: String], extraFallbackDirectories: [String] = ["/usr/local/bin", "/opt/homebrew/bin"], fileExists: (String) -> Bool = { FileManager.default.isExecutableFile(atPath: $0) }) -> Resolution }`. An `executable` containing `/` is treated as an explicit path (resolved iff executable).
  - `struct ContainerCLI: Sendable` with `init(executable: String = "container", environment: [String: String] = ProcessInfo.processInfo.environment)`.
  - `func checkAvailable() async -> Bool` (runs `container --version`).
  - `static func runArguments(containerName: String, imageName: String, specs: [LinuxRunSpec], env: [String: String]) -> [String]` — pure, returns the full `container run …` arg list including `--scheme http`.
  - `static func deleteArguments(containerName: String) -> [String]` → `["delete", "--force", <name>]`.
  - `func pull(image: String) async throws`, `func runAttached(containerName:imageName:specs:env:terminationHandler:) throws -> (Process, Pipe, Pipe)`, `func stop(containerName:) async throws`, `func delete(containerName:) async throws`, `func list() async throws -> [LinuxContainerInfo]` (parses `container list --all --format json`, filters `wendy.managed=true`).

- [ ] **Step 1: Write the failing tests**

```swift
// swift/WendyAgentCore/Tests/WendyAgentTests/ExecutableResolverTests.swift
import Testing

@testable import WendyAgentCore

@Suite struct ExecutableResolverTests {
    @Test func resolvesFromPathFirst() {
        let r = ExecutableResolver.resolve(
            "container",
            environment: ["PATH": "/custom/bin:/usr/bin"],
            extraFallbackDirectories: ["/opt/homebrew/bin"],
            fileExists: { $0 == "/custom/bin/container" }
        )
        #expect(r.resolvedPath == "/custom/bin/container")
    }

    @Test func fallsBackToExtraDirectoriesWhenNotOnPath() {
        let r = ExecutableResolver.resolve(
            "container",
            environment: ["PATH": "/usr/bin"],
            extraFallbackDirectories: ["/opt/homebrew/bin"],
            fileExists: { $0 == "/opt/homebrew/bin/container" }
        )
        #expect(r.resolvedPath == "/opt/homebrew/bin/container")
    }

    @Test func nilWhenNowhereExecutable() {
        let r = ExecutableResolver.resolve(
            "container", environment: ["PATH": "/usr/bin"],
            extraFallbackDirectories: ["/opt/homebrew/bin"], fileExists: { _ in false }
        )
        #expect(r.resolvedPath == nil)
        #expect(!r.searchedPaths.isEmpty)
    }

    @Test func explicitPathHonored() {
        let r = ExecutableResolver.resolve(
            "/abs/container", environment: [:], fileExists: { $0 == "/abs/container" }
        )
        #expect(r.resolvedPath == "/abs/container")
    }
}
```

```swift
// swift/WendyAgentCore/Tests/WendyAgentTests/ContainerCLITests.swift
import Testing

@testable import WendyAgentCore

@Suite struct ContainerCLITests {
    @Test func runArgumentsIncludeSchemeNameLabelAndImageLast() {
        let args = ContainerCLI.runArguments(
            containerName: "wendy-app",
            imageName: "localhost:5555/app:latest",
            specs: [.publishPort(host: 8080, container: 80), .volume(name: "wendy-app-data", path: "/data")],
            env: ["FOO": "bar"]
        )
        // Image must be the final positional argument.
        #expect(args.last == "localhost:5555/app:latest")
        #expect(args.first == "run")
        #expect(args.contains("--scheme"))
        #expect(argFollowing("--scheme", in: args) == "http")
        #expect(argFollowing("--name", in: args) == "wendy-app")
        #expect(args.contains("--label"))
        #expect(argFollowing("--label", in: args) == "wendy.managed=true")
        #expect(pairPresent("-p", "8080:80", in: args))
        #expect(pairPresent("-v", "wendy-app-data:/data", in: args))
        #expect(pairPresent("-e", "FOO=bar", in: args))
    }

    @Test func networkNoneRendersNetworkFlag() {
        let args = ContainerCLI.runArguments(
            containerName: "wendy-app", imageName: "img", specs: [.networkNone], env: [:]
        )
        #expect(pairPresent("--network", "none", in: args))
    }

    @Test func deleteArgumentsForce() {
        #expect(ContainerCLI.deleteArguments(containerName: "wendy-app") == ["delete", "--force", "wendy-app"])
    }
}

private func argFollowing(_ flag: String, in args: [String]) -> String? {
    guard let i = args.firstIndex(of: flag), i + 1 < args.count else { return nil }
    return args[i + 1]
}

private func pairPresent(_ flag: String, _ value: String, in args: [String]) -> Bool {
    var i = args.startIndex
    while let j = args[i...].firstIndex(of: flag) {
        if j + 1 < args.count, args[j + 1] == value { return true }
        i = args.index(after: j)
    }
    return false
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd swift/WendyAgentCore && swift test --filter ExecutableResolverTests --filter ContainerCLITests`
Expected: FAIL — `ExecutableResolver` / `ContainerCLI` not defined.

- [ ] **Step 3a: Implement `ExecutableResolver`**

```swift
// swift/WendyAgentCore/Sources/WendyAgent/Containers/ExecutableResolver.swift
import Foundation

/// Resolves a CLI tool's absolute path: entries on `PATH` first, then a set of
/// fallback directories (homebrew/usr-local). Shared by `DockerCLI` and
/// `ContainerCLI` so the lookup logic exists once.
enum ExecutableResolver {
    struct Resolution: Sendable {
        let resolvedPath: String?
        let searchedPaths: [String]
    }

    static func resolve(
        _ executable: String,
        environment: [String: String],
        extraFallbackDirectories: [String] = ["/usr/local/bin", "/opt/homebrew/bin"],
        fileExists: (String) -> Bool = { FileManager.default.isExecutableFile(atPath: $0) }
    ) -> Resolution {
        // An explicit path is used as-is.
        if executable.contains("/") {
            return Resolution(
                resolvedPath: fileExists(executable) ? executable : nil,
                searchedPaths: [executable]
            )
        }
        let pathDirs = (environment["PATH"] ?? "")
            .split(separator: ":").map(String.init).filter { !$0.isEmpty }
        var candidates: [String] = []
        var seen = Set<String>()
        for dir in pathDirs + extraFallbackDirectories {
            let candidate = URL(fileURLWithPath: dir).appendingPathComponent(executable).path
            if seen.insert(candidate).inserted { candidates.append(candidate) }
        }
        return Resolution(
            resolvedPath: candidates.first(where: fileExists),
            searchedPaths: candidates
        )
    }
}
```

Then refactor `DockerCLI` to use it. In `DockerCLI.swift`, replace the bodies of the private `resolveExecutable()`/`buildSearchPaths()`/`fallbackExecutablePaths()` (lines ~347-394) with a single call site: keep `resolveExecutablePath()` and `resolveExecutableForTesting()`, but implement them via `ExecutableResolver.resolve(self.executable, environment: self.environment, extraFallbackDirectories: ["/usr/local/bin", "/opt/homebrew/bin", "/Applications/Docker.app/Contents/Resources/bin"])`. Map a nil resolution to `DockerError.executableNotFound`. Delete the now-dead private helpers. The existing `DockerCLI` tests (`resolveExecutableForTesting`) must still pass unchanged.

- [ ] **Step 3b: Implement `ContainerCLI`**

Short commands go through the existing `Subprocess.run` (resolve the path with `ExecutableResolver`, throw on nonzero exit). Only `runAttached` keeps its own small process setup (it must return the live process + pipes for streaming, which `Subprocess.run` — which reads to EOF — cannot do).

```swift
// swift/WendyAgentCore/Sources/WendyAgent/Containers/ContainerCLI.swift
import Foundation
import Logging

/// A thin wrapper around Apple's `container` CLI. Mirrors `DockerCLI`.
struct ContainerCLI: Sendable {
    private let logger = Logger(label: "sh.wendy.agent.container-cli")
    private let executable: String
    private let environment: [String: String]

    init(
        executable: String = "container",
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) {
        self.executable = executable
        self.environment = environment
    }

    // MARK: - Pure argument builders (unit-tested)

    /// Full argument list for `container run` in attached mode. `--scheme http`
    /// lets the runtime pull from the insecure localhost registry. The image is
    /// always the final positional argument.
    static func runArguments(
        containerName: String,
        imageName: String,
        specs: [LinuxRunSpec],
        env: [String: String]
    ) -> [String] {
        var args = [
            "run",
            "--name", containerName,
            "--label", "wendy.managed=true",
            "--scheme", "http",
        ]
        for (key, value) in env.sorted(by: { $0.key < $1.key }) {
            args += ["-e", "\(key)=\(value)"]
        }
        for spec in specs {
            switch spec {
            case .networkNone:
                args += ["--network", "none"]
            case .publishPort(let host, let container):
                args += ["-p", "\(host):\(container)"]
            case .volume(let name, let path):
                args += ["-v", "\(name):\(path)"]
            }
        }
        args.append(imageName)
        return args
    }

    static func deleteArguments(containerName: String) -> [String] {
        ["delete", "--force", containerName]
    }

    // MARK: - Availability

    func checkAvailable() async -> Bool {
        (try? await run(["--version"])) != nil
    }

    // MARK: - Image + lifecycle

    func pull(image: String) async throws {
        _ = try await run(["pull", "--scheme", "http", image])
    }

    func runAttached(
        containerName: String,
        imageName: String,
        specs: [LinuxRunSpec],
        env: [String: String],
        terminationHandler: (@Sendable (Foundation.Process) -> Void)?
    ) throws -> (process: Foundation.Process, stdout: Pipe, stderr: Pipe) {
        let args = Self.runArguments(
            containerName: containerName, imageName: imageName, specs: specs, env: env
        )
        let resolved = try resolvedExecutablePath()
        let process = Foundation.Process()
        process.executableURL = URL(fileURLWithPath: resolved)
        process.arguments = args
        process.environment = environment
        process.terminationHandler = terminationHandler
        let out = Pipe()
        let err = Pipe()
        process.standardOutput = out
        process.standardError = err
        try process.run()
        return (process, out, err)
    }

    func stop(containerName: String) async throws {
        _ = try await run(["stop", containerName])
    }

    func delete(containerName: String) async throws {
        _ = try await run(Self.deleteArguments(containerName: containerName))
    }

    func list() async throws -> [LinuxContainerInfo] {
        let output = try await run(["list", "--all", "--format", "json"])
        return Self.parseList(output)
    }

    /// Parse `container list --format json` output, keeping Wendy-managed
    /// containers. `container`'s JSON nests config under `configuration`; be
    /// lenient about shape and fall back to top-level keys.
    static func parseList(_ output: String) -> [LinuxContainerInfo] {
        guard let data = output.data(using: .utf8),
            let array = try? JSONSerialization.jsonObject(with: data) as? [[String: Any]]
        else { return [] }
        return array.compactMap { entry -> LinuxContainerInfo? in
            let config = (entry["configuration"] as? [String: Any]) ?? entry
            let labels = (config["labels"] as? [String: Any]) ?? (entry["labels"] as? [String: Any]) ?? [:]
            guard "\(labels["wendy.managed"] ?? "")" == "true" else { return nil }
            let id = "\(config["id"] ?? entry["id"] ?? "")"
            let state = "\(entry["status"] ?? entry["state"] ?? "")"
            guard !id.isEmpty else { return nil }
            return LinuxContainerInfo(id: id, name: id, state: state)
        }
    }

    // MARK: - Private

    private func resolvedExecutablePath() throws -> String {
        let resolution = ExecutableResolver.resolve(executable, environment: environment)
        guard let path = resolution.resolvedPath else {
            throw ContainerCLIError.executableNotFound(
                executable: executable, searchedPaths: resolution.searchedPaths
            )
        }
        return path
    }

    /// Run a short `container` command via the shared `Subprocess` helper;
    /// throw on nonzero exit. Long-running attached runs use `runAttached`.
    @discardableResult
    private func run(_ arguments: [String]) async throws -> String {
        let resolved = try resolvedExecutablePath()
        let result = try await Subprocess.run(resolved, arguments)
        guard result.status == 0 else {
            throw ContainerCLIError.commandFailed(
                executable: resolved, args: arguments, status: result.status,
                stderr: result.stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            )
        }
        return result.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

enum ContainerCLIError: Error, CustomStringConvertible {
    case executableNotFound(executable: String, searchedPaths: [String])
    case commandFailed(executable: String, args: [String], status: Int32, stderr: String)

    var description: String {
        switch self {
        case .executableNotFound(let executable, let searchedPaths):
            return "Could not find \(executable). Looked in: \(searchedPaths.joined(separator: ", "))"
        case .commandFailed(let executable, let args, let status, let stderr):
            let cmd = ([executable] + args).joined(separator: " ")
            return stderr.isEmpty
                ? "\(cmd) exited with status \(status)"
                : "\(cmd) exited with status \(status): \(stderr)"
        }
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd swift/WendyAgentCore && swift test --filter ExecutableResolverTests --filter ContainerCLITests && swift test --filter DockerCLI && swift build`
Expected: resolver tests PASS (4), ContainerCLI tests PASS (3), existing DockerCLI tests still PASS, build clean.

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Containers/ExecutableResolver.swift \
        swift/WendyAgentCore/Sources/WendyAgent/Containers/ContainerCLI.swift \
        swift/WendyAgentCore/Sources/WendyAgent/Docker/DockerCLI.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/ExecutableResolverTests.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/ContainerCLITests.swift
git commit -m "feat(mac): shared ExecutableResolver + ContainerCLI over Apple container"
```

---

### Task 3: `ContainerCLIBackend` conforming to `LinuxContainerBackend`

Wraps `ContainerCLI` behind the protocol: interpret entitlements → specs, remove stale container, run attached.

**Files:**
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Containers/ContainerCLIBackend.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/ContainerCLIBackendTests.swift`

**Interfaces:**
- Consumes: `ContainerCLI` (Task 2), `LinuxContainerBackend`/`LinuxRunSpecBuilder`/`managedContainerName` (Task 1), `WendyAppConfig` env: this task also reads `appConfig?.entitlements`.
- Produces: `actor ContainerCLIBackend: LinuxContainerBackend` with `init(cli: ContainerCLI = ContainerCLI())`. Also `nonisolated static func specs(for appConfig: WendyAppConfig?, appName: String, warn: (String) -> Void) -> [LinuxRunSpec]` delegating to `LinuxRunSpecBuilder` (exposed for testing without a runtime).

- [ ] **Step 1: Write the failing test**

```swift
// swift/WendyAgentCore/Tests/WendyAgentTests/ContainerCLIBackendTests.swift
import Testing

@testable import WendyAgentCore

@Suite struct ContainerCLIBackendTests {
    @Test func specsForConfigMapNetworkAndPersist() {
        let config = WendyAppConfig(
            appId: "app",
            platform: "linux/arm64",
            entitlements: [
                WendyEntitlement(
                    type: "network", mode: nil, name: nil, path: nil,
                    ports: [WendyPortMapping(host: 3000, container: 3000)]
                ),
                WendyEntitlement(type: "persist", mode: nil, name: "db", path: "/data", ports: nil),
            ],
            brewfile: nil
        )
        let specs = ContainerCLIBackend.specs(for: config, appName: "svc", warn: { _ in })
        #expect(specs.contains(.publishPort(host: 3000, container: 3000)))
        #expect(specs.contains(.volume(name: "wendy-svc-db", path: "/data")))
    }

    @Test func specsForNilConfigAreEmpty() {
        #expect(ContainerCLIBackend.specs(for: nil, appName: "svc", warn: { _ in }).isEmpty)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter ContainerCLIBackendTests`
Expected: FAIL — `ContainerCLIBackend` not defined.

- [ ] **Step 3: Write minimal implementation**

```swift
// swift/WendyAgentCore/Sources/WendyAgent/Containers/ContainerCLIBackend.swift
import Foundation
import Logging

/// Runs Linux containers via Apple's `container` CLI.
actor ContainerCLIBackend: LinuxContainerBackend {
    private let cli: ContainerCLI
    private let logger = Logger(label: "sh.wendy.agent.container-backend")

    init(cli: ContainerCLI = ContainerCLI()) { self.cli = cli }

    nonisolated static func specs(
        for appConfig: WendyAppConfig?,
        appName: String,
        warn: (String) -> Void
    ) -> [LinuxRunSpec] {
        LinuxRunSpecBuilder.specs(
            from: appConfig?.entitlements ?? [], appName: appName, warn: warn
        )
    }

    func pull(image: String) async throws {
        logger.info("Pulling image", metadata: ["image": "\(image)"])
        try await cli.pull(image: image)
    }

    func createAndStart(
        appName: String,
        imageName: String,
        appConfig: WendyAppConfig?,
        terminationHandler: (@Sendable (Foundation.Process) -> Void)?
    ) async throws -> (process: Foundation.Process, stdout: Pipe, stderr: Pipe) {
        let name = managedContainerName(for: appName)
        try? await cli.delete(containerName: name)  // clear any stale container
        let specs = Self.specs(for: appConfig, appName: appName) { [logger] message in
            logger.warning("\(message)", metadata: ["app_name": "\(appName)"])
        }
        logger.info(
            "Starting container", metadata: ["container": "\(name)", "image": "\(imageName)"]
        )
        return try cli.runAttached(
            containerName: name,
            imageName: imageName,
            specs: specs,
            env: [:],
            terminationHandler: terminationHandler
        )
    }

    func stop(appName: String) async throws {
        try? await cli.stop(containerName: managedContainerName(for: appName))
    }

    func remove(appName: String) async throws {
        try? await cli.delete(containerName: managedContainerName(for: appName))
    }

    func listContainers() async throws -> [LinuxContainerInfo] {
        try await cli.list()
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd swift/WendyAgentCore && swift test --filter ContainerCLIBackendTests`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Containers/ContainerCLIBackend.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/ContainerCLIBackendTests.swift
git commit -m "feat(mac): ContainerCLIBackend over Apple container runtime"
```

---

### Task 4: Finish `DockerContainerBackend` and conform it to `LinuxContainerBackend`

Wire the currently-dead `createAndStart`/`pullImage`, reuse the shared `LinuxRunSpec` mapping, and make it conform to the protocol so `ContainerService` can hold either backend behind one type.

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Docker/DockerContainerBackend.swift` (whole file)
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Docker/DockerCLI.swift:213-227` (make `ps` reusable — return `LinuxContainerInfo`) — see note.
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/DockerContainerBackendTests.swift`

**Interfaces:**
- Consumes: `LinuxContainerBackend`, `LinuxRunSpec`, `LinuxRunSpecBuilder`, `managedContainerName`, `LinuxContainerInfo` (Task 1); existing `DockerCLI`.
- Produces: `actor DockerContainerBackend: LinuxContainerBackend`. Adds `nonisolated static func runOptions(for appConfig: WendyAppConfig?, appName: String, warn: (String) -> Void) -> [DockerCLI.RunOption]` that renders `[LinuxRunSpec]` to `DockerCLI.RunOption` (`.publish`, `.volume`, `.network`), plus `.rm`, `.name`, `.label(wendy.managed=true)`, `.label(wendy.app-name=…)`.

- [ ] **Step 1: Write the failing test**

```swift
// swift/WendyAgentCore/Tests/WendyAgentTests/DockerContainerBackendTests.swift
import Testing

@testable import WendyAgentCore

@Suite struct DockerContainerBackendTests {
    @Test func runOptionsRenderPortsVolumesAndManagedLabels() {
        let config = WendyAppConfig(
            appId: "app",
            platform: "linux/arm64",
            entitlements: [
                WendyEntitlement(
                    type: "network", mode: nil, name: nil, path: nil,
                    ports: [WendyPortMapping(host: 8080, container: 80)]
                ),
                WendyEntitlement(type: "persist", mode: nil, name: "data", path: "/data", ports: nil),
            ],
            brewfile: nil
        )
        let opts = DockerContainerBackend.runOptions(for: config, appName: "app", warn: { _ in })
        let args = opts.flatMap(\.arguments)
        #expect(args.contains("wendy.managed=true"))
        #expect(args.contains("8080:80"))
        #expect(args.contains("wendy-app-data:/data"))
        #expect(args.contains("wendy-app"))  // --name value
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter DockerContainerBackendTests`
Expected: FAIL — `runOptions(for:appName:warn:)` not defined.

- [ ] **Step 3: Write minimal implementation**

First make `DockerCLI.ps` return the shared type. In `DockerCLI.swift`, keep the `ContainerInfo` struct but add a mapping, or change `ps` to return `[LinuxContainerInfo]` mapping `id`→`id`, `names`→`name`, `state`→`state` (drop `status`). Update the two existing `ps` callers accordingly (only `DockerContainerBackend.listContainers`).

Then rewrite `DockerContainerBackend.swift`:

```swift
// swift/WendyAgentCore/Sources/WendyAgent/Docker/DockerContainerBackend.swift
import Foundation
import Logging

/// Runs Linux containers via Docker on a Mac agent.
actor DockerContainerBackend: LinuxContainerBackend {
    private let docker: DockerCLI
    private let logger = Logger(label: "sh.wendy.agent.docker-backend")

    init(docker: DockerCLI = DockerCLI()) { self.docker = docker }

    nonisolated static func runOptions(
        for appConfig: WendyAppConfig?,
        appName: String,
        warn: (String) -> Void
    ) -> [DockerCLI.RunOption] {
        var options: [DockerCLI.RunOption] = [
            .rm,
            .name(managedContainerName(for: appName)),
            .label(key: "wendy.managed", value: "true"),
            .label(key: "wendy.app-name", value: appName),
        ]
        let specs = LinuxRunSpecBuilder.specs(
            from: appConfig?.entitlements ?? [], appName: appName, warn: warn
        )
        for spec in specs {
            switch spec {
            case .networkNone: options.append(.network("none"))
            case .publishPort(let h, let c): options.append(.publish(hostPort: h, containerPort: c))
            case .volume(let name, let path): options.append(.volume(hostOrName: name, containerPath: path))
            }
        }
        return options
    }

    func pull(image: String) async throws {
        logger.info("Pulling image", metadata: ["image": "\(image)"])
        try await docker.pull(image: image)
    }

    func createAndStart(
        appName: String,
        imageName: String,
        appConfig: WendyAppConfig?,
        terminationHandler: (@Sendable (Foundation.Process) -> Void)?
    ) async throws -> (process: Foundation.Process, stdout: Pipe, stderr: Pipe) {
        let name = managedContainerName(for: appName)
        _ = try? await docker.rm(options: [.force], container: name)
        let options = Self.runOptions(for: appConfig, appName: appName) { [logger] message in
            logger.warning("\(message)", metadata: ["app_name": "\(appName)"])
        }
        logger.info(
            "Starting Docker container",
            metadata: ["container": "\(name)", "image": "\(imageName)"]
        )
        return try docker.runAttached(
            options: options, image: imageName, terminationHandler: terminationHandler
        )
    }

    func stop(appName: String) async throws {
        _ = try? await docker.stop(container: managedContainerName(for: appName), timeout: 10)
    }

    func remove(appName: String) async throws {
        _ = try? await docker.rm(options: [.force], container: managedContainerName(for: appName))
    }

    func listContainers() async throws -> [LinuxContainerInfo] {
        try await docker.ps(label: "wendy.managed=true")
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd swift/WendyAgentCore && swift test --filter DockerContainerBackendTests`
Expected: PASS. Then `swift build` to confirm the `ps`/callers change compiles.

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Docker/DockerContainerBackend.swift \
        swift/WendyAgentCore/Sources/WendyAgent/Docker/DockerCLI.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/DockerContainerBackendTests.swift
git commit -m "feat(mac): finish DockerContainerBackend + conform to LinuxContainerBackend"
```

---

### Task 5: Embedded OCI Distribution registry (Hummingbird)

Add Hummingbird and implement a minimal OCI Distribution v2 server on `127.0.0.1:5555` backed by the agent's blob store. Receives the CLI's push; serves pulls to `container`/`docker`.

**Files:**
- Modify: `swift/WendyAgentCore/Package.swift` (add Hummingbird dependency + product)
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Registry/BlobStore.swift`
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Registry/AgentImageRegistry.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/BlobStoreTests.swift`

**Interfaces:**
- Produces:
  - `struct BlobStore: Sendable` over a root directory with `init(root: URL)`, `func hasBlob(digest: String) -> Bool`, `func blobURL(digest: String) -> URL`, `func writeBlob(_ data: Data, expectedDigest: String) throws` (verifies sha256), `func writeManifest(_ data: Data, repository: String, reference: String) throws`, `func manifestURL(repository: String, reference: String) -> URL?`, `func putUpload() -> UUID`, upload-append/commit helpers. Layout matches the existing content store: blobs at `<root>/blobs/sha256/<hex>`; manifests at `<root>/manifests/<repo>/<reference>`.
  - `struct AgentImageRegistry: Sendable` with `init(store: BlobStore, port: Int = 5555)` and `func run() async throws` (blocks serving; started as a background task by the agent).

- [ ] **Step 1: Write the failing test** (BlobStore is the unit-testable core; the HTTP layer is exercised by the Task 9 E2E)

```swift
// swift/WendyAgentCore/Tests/WendyAgentTests/BlobStoreTests.swift
import Crypto
import Foundation
import Testing

@testable import WendyAgentCore

@Suite struct BlobStoreTests {
    private func tempRoot() -> URL {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("blobstore-\(UUID().uuidString)")
        try? FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    @Test func writeAndReadBlobByDigest() throws {
        let store = BlobStore(root: tempRoot())
        let payload = Data("hello".utf8)
        let hex = SHA256.hash(data: payload).map { String(format: "%02x", $0) }.joined()
        let digest = "sha256:\(hex)"
        try store.writeBlob(payload, expectedDigest: digest)
        #expect(store.hasBlob(digest: digest))
        #expect(try Data(contentsOf: store.blobURL(digest: digest)) == payload)
    }

    @Test func writeBlobRejectsDigestMismatch() {
        let store = BlobStore(root: tempRoot())
        #expect(throws: (any Error).self) {
            try store.writeBlob(Data("hello".utf8), expectedDigest: "sha256:deadbeef")
        }
    }

    @Test func manifestRoundTripByTag() throws {
        let store = BlobStore(root: tempRoot())
        let manifest = Data(#"{"schemaVersion":2}"#.utf8)
        try store.writeManifest(manifest, repository: "app", reference: "latest")
        let url = try #require(store.manifestURL(repository: "app", reference: "latest"))
        #expect(try Data(contentsOf: url) == manifest)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter BlobStoreTests`
Expected: FAIL — `BlobStore` not defined.

- [ ] **Step 3a: Add Hummingbird to Package.swift**

In `swift/WendyAgentCore/Package.swift`, add to `dependencies`:

```swift
        .package(url: "https://github.com/hummingbird-project/hummingbird.git", from: "2.0.0"),
```

and to the `WendyAgentCore` target's `dependencies`:

```swift
                .product(name: "Hummingbird", package: "hummingbird"),
```

- [ ] **Step 3b: Implement `BlobStore`**

```swift
// swift/WendyAgentCore/Sources/WendyAgent/Registry/BlobStore.swift
import Crypto
import Foundation

/// On-disk content store shared by the registry and the WriteLayer RPC.
/// Blobs live at `<root>/blobs/sha256/<hex>`; manifests at
/// `<root>/manifests/<repo>/<reference>`.
struct BlobStore: Sendable {
    let root: URL

    init(root: URL) {
        self.root = root
        try? FileManager.default.createDirectory(
            at: root.appendingPathComponent("blobs/sha256"), withIntermediateDirectories: true
        )
    }

    enum BlobError: Error { case digestMismatch(expected: String, actual: String); case badDigest(String) }

    func blobURL(digest: String) -> URL {
        root.appendingPathComponent("blobs/\(digest.replacingOccurrences(of: ":", with: "/"))")
    }

    func hasBlob(digest: String) -> Bool {
        FileManager.default.fileExists(atPath: blobURL(digest: digest).path)
    }

    func writeBlob(_ data: Data, expectedDigest: String) throws {
        let hex = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        let actual = "sha256:\(hex)"
        guard actual == expectedDigest.lowercased() else {
            throw BlobError.digestMismatch(expected: expectedDigest, actual: actual)
        }
        let url = blobURL(digest: actual)
        try FileManager.default.createDirectory(
            at: url.deletingLastPathComponent(), withIntermediateDirectories: true
        )
        try data.write(to: url, options: .atomic)
    }

    func manifestURL(repository: String, reference: String) -> URL? {
        let url = manifestPath(repository: repository, reference: reference)
        return FileManager.default.fileExists(atPath: url.path) ? url : nil
    }

    func writeManifest(_ data: Data, repository: String, reference: String) throws {
        let url = manifestPath(repository: repository, reference: reference)
        try FileManager.default.createDirectory(
            at: url.deletingLastPathComponent(), withIntermediateDirectories: true
        )
        try data.write(to: url, options: .atomic)
        // Also index by content digest so pulls by digest resolve.
        let hex = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        let byDigest = manifestPath(repository: repository, reference: "sha256:\(hex)")
        try? data.write(to: byDigest, options: .atomic)
    }

    private func manifestPath(repository: String, reference: String) -> URL {
        root.appendingPathComponent("manifests")
            .appendingPathComponent(repository)
            .appendingPathComponent(reference.replacingOccurrences(of: ":", with: "_"))
    }
}
```

- [ ] **Step 3c: Implement `AgentImageRegistry`** (HTTP layer; verified end-to-end in Task 9)

```swift
// swift/WendyAgentCore/Sources/WendyAgent/Registry/AgentImageRegistry.swift
import Foundation
import Hummingbird
import Logging
import NIOCore

/// Minimal OCI Distribution v2 server backed by `BlobStore`. Handles the subset
/// docker/container use to push and pull: version check, blob uploads
/// (monolithic + chunked), blob read, manifest put/get.
struct AgentImageRegistry: Sendable {
    private let store: BlobStore
    private let port: Int
    private let logger = Logger(label: "sh.wendy.agent.registry")
    private let uploads = UploadBuffers()

    init(store: BlobStore, port: Int = 5555) {
        self.store = store
        self.port = port
    }

    /// Accumulates in-progress chunked uploads keyed by upload UUID.
    private actor UploadBuffers {
        private var buffers: [String: Data] = [:]
        func start() -> String { let id = UUID().uuidString; buffers[id] = Data(); return id }
        func append(_ data: Data, to id: String) { buffers[id, default: Data()].append(data) }
        func take(_ id: String) -> Data? { buffers.removeValue(forKey: id) }
    }

    func run() async throws {
        let router = Router()
        let store = self.store
        let uploads = self.uploads

        router.get("/v2/") { _, _ in Response(status: .ok) }
        router.get("/v2") { _, _ in Response(status: .ok) }

        // Begin an upload: return a session URL in Location.
        router.post("/v2/:repo/blobs/uploads") { request, _ -> Response in
            // Monolithic push: POST ...?digest=<d> with the full body.
            if let digest = request.uri.queryParameters["digest"].map(String.init) {
                let data = try await collect(request.body)
                try store.writeBlob(data, expectedDigest: digest)
                return Response(status: .created, headers: [.location: "/v2/\(request.parameters.get("repo") ?? "")/blobs/\(digest)"])
            }
            let id = await uploads.start()
            let repo = request.parameters.get("repo") ?? ""
            return Response(status: .accepted, headers: [
                .location: "/v2/\(repo)/blobs/uploads/\(id)",
                .init("Docker-Upload-UUID")!: id,
            ])
        }

        // Chunk append (PATCH) — optional path used by chunked pushers.
        router.patch("/v2/:repo/blobs/uploads/:id") { request, _ -> Response in
            let id = request.parameters.get("id") ?? ""
            await uploads.append(try await collect(request.body), to: id)
            let repo = request.parameters.get("repo") ?? ""
            return Response(status: .accepted, headers: [.location: "/v2/\(repo)/blobs/uploads/\(id)"])
        }

        // Commit upload (PUT ...?digest=<d>), optionally with a final chunk body.
        router.put("/v2/:repo/blobs/uploads/:id") { request, _ -> Response in
            let id = request.parameters.get("id") ?? ""
            let digest = request.uri.queryParameters["digest"].map(String.init) ?? ""
            var data = await uploads.take(id) ?? Data()
            data.append(try await collect(request.body))
            try store.writeBlob(data, expectedDigest: digest)
            let repo = request.parameters.get("repo") ?? ""
            return Response(status: .created, headers: [.location: "/v2/\(repo)/blobs/\(digest)"])
        }

        router.on("/v2/:repo/blobs/:digest", method: .head) { request, _ -> Response in
            let digest = request.parameters.get("digest") ?? ""
            return store.hasBlob(digest: digest) ? Response(status: .ok) : Response(status: .notFound)
        }

        router.get("/v2/:repo/blobs/:digest") { request, _ -> Response in
            let digest = request.parameters.get("digest") ?? ""
            guard store.hasBlob(digest: digest),
                let data = try? Data(contentsOf: store.blobURL(digest: digest))
            else { return Response(status: .notFound) }
            return Response(
                status: .ok,
                headers: [.contentType: "application/octet-stream"],
                body: .init(byteBuffer: ByteBuffer(bytes: data))
            )
        }

        router.put("/v2/:repo/manifests/:reference") { request, _ -> Response in
            let repo = request.parameters.get("repo") ?? ""
            let reference = request.parameters.get("reference") ?? ""
            let data = try await collect(request.body)
            try store.writeManifest(data, repository: repo, reference: reference)
            return Response(status: .created)
        }

        router.on("/v2/:repo/manifests/:reference", method: .head) { request, _ -> Response in
            let repo = request.parameters.get("repo") ?? ""
            let reference = request.parameters.get("reference") ?? ""
            return store.manifestURL(repository: repo, reference: reference) != nil
                ? Response(status: .ok) : Response(status: .notFound)
        }

        router.get("/v2/:repo/manifests/:reference") { request, _ -> Response in
            let repo = request.parameters.get("repo") ?? ""
            let reference = request.parameters.get("reference") ?? ""
            guard let url = store.manifestURL(repository: repo, reference: reference),
                let data = try? Data(contentsOf: url)
            else { return Response(status: .notFound) }
            // Content-Type must echo the stored manifest's mediaType; default to OCI.
            let mediaType =
                (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["mediaType"]
                as? String ?? "application/vnd.oci.image.manifest.v1+json"
            return Response(
                status: .ok,
                headers: [.contentType: mediaType],
                body: .init(byteBuffer: ByteBuffer(bytes: data))
            )
        }

        let app = Application(
            router: router,
            configuration: .init(address: .hostname("127.0.0.1", port: port))
        )
        logger.info("Agent image registry listening", metadata: ["port": "\(port)"])
        try await app.runService()
    }
}

/// Collect a Hummingbird request body into `Data`.
private func collect(_ body: RequestBody) async throws -> Data {
    var data = Data()
    for try await chunk in body {
        data.append(contentsOf: chunk.readableBytesView)
    }
    return data
}
```

Note: Hummingbird 2's exact type names (`Response(status:headers:body:)`, `.init(byteBuffer:)`, `request.uri.queryParameters`, `request.parameters.get`, `Router`, `Application.runService()`) should be confirmed against the resolved Hummingbird 2 version and the `hummingbird` skill. Adjust header key construction (`HTTPField.Name`) as the API requires. The behavior contract — the routes, statuses, digest verification, and Location headers above — is what must hold.

- [ ] **Step 4: Run test to verify BlobStore passes; build to confirm the registry compiles**

Run: `cd swift/WendyAgentCore && swift test --filter BlobStoreTests && swift build`
Expected: BlobStore tests PASS; build succeeds with Hummingbird resolved.

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Package.swift swift/WendyAgentCore/Package.resolved \
        swift/WendyAgentCore/Sources/WendyAgent/Registry/ \
        swift/WendyAgentCore/Tests/WendyAgentTests/BlobStoreTests.swift
git commit -m "feat(mac): embedded OCI distribution registry over agent blob store"
```

---

### Task 6: Wire the Linux path into `ContainerService`

Replace the `dockerBackend`/`throw` gate with a real `linuxBackend` path: register `.container` apps, pull + create + start streaming logs through the existing machinery, and route stop/remove/list.

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Services/ContainerService.swift` (init 30-75; createContainer 736-741; startContainer 851-873, 947-1002; stopTrackedAppIfRunning 314-315; deleteContainer path ~1039-1040)
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/ContainerServiceLinuxTests.swift`

**Interfaces:**
- Consumes: `any LinuxContainerBackend` (Tasks 1/3/4), `WendyApp.ContainerMetadata`, `registerApp`, `markAppRunning`, `prepareAppForLaunch`, `cancelAppLaunch`, `makeTerminationHandler`, the streaming block.
- Produces: `ContainerService.init` gains `linuxBackend: (any LinuxContainerBackend)? = nil` replacing `dockerBackend`/`dockerAvailable`. `createContainer` registers `.container`; `startContainer` runs it.

- [ ] **Step 1: Write the failing test**

```swift
// swift/WendyAgentCore/Tests/WendyAgentTests/ContainerServiceLinuxTests.swift
import Foundation
import Testing

@testable import WendyAgentCore

/// A fake backend that records calls and hands back a short-lived process.
actor FakeLinuxBackend: LinuxContainerBackend {
    private(set) var pulled: [String] = []
    private(set) var started: [String] = []
    private(set) var stopped: [String] = []

    func pull(image: String) async throws { pulled.append(image) }

    func createAndStart(
        appName: String, imageName: String, appConfig: WendyAppConfig?,
        terminationHandler: (@Sendable (Foundation.Process) -> Void)?
    ) async throws -> (process: Foundation.Process, stdout: Pipe, stderr: Pipe) {
        started.append(appName)
        let p = Foundation.Process()
        p.executableURL = URL(fileURLWithPath: "/bin/echo")
        p.arguments = ["hi"]
        let out = Pipe(); let err = Pipe()
        p.standardOutput = out; p.standardError = err
        p.terminationHandler = terminationHandler
        try p.run()
        return (p, out, err)
    }

    func stop(appName: String) async throws { stopped.append(appName) }
    func remove(appName: String) async throws {}
    func listContainers() async throws -> [LinuxContainerInfo] { [] }

    func pulledImages() -> [String] { pulled }
    func startedApps() -> [String] { started }
}

@Suite struct ContainerServiceLinuxTests {
    @Test func createThenStartPullsAndRunsViaBackend() async throws {
        let backend = FakeLinuxBackend()
        let service = ContainerService(
            broadcaster: TelemetryBroadcaster(),
            executablePath: "/usr/bin/true",
            stateDirectory: FileManager.default.temporaryDirectory
                .appendingPathComponent("cs-\(UUID().uuidString)"),
            linuxBackend: backend
        )
        let config = WendyAppConfig(appId: "svc", platform: "linux/arm64", entitlements: nil, brewfile: nil)
        let configData = try JSONEncoder().encode(config)

        // createContainer registers a .container app (no throw).
        var createReq = Wendy_Agent_Services_V1_CreateContainerRequest()
        createReq.appName = "svc"
        createReq.imageName = "localhost:5555/svc:latest"
        createReq.appConfig = configData
        _ = try await service.createContainer(
            request: .init(message: createReq), context: TestServerContext()
        )
        let infos = service.currentAppInfosForTesting()
        #expect(infos.contains { $0.id == "svc" && $0.kind == .container })
    }
}
```

(If constructing `ServerContext`/`ServerRequest` in tests is awkward, split `createContainer`'s core into an internal `func createContainerCore(appName:imageName:appConfigData:) async throws` and test that instead — keep the RPC method a thin wrapper. Add `TestServerContext` only if the existing suite already has such a helper; otherwise use the core-function approach.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter ContainerServiceLinuxTests`
Expected: FAIL — `linuxBackend:` initializer parameter doesn't exist / Linux create still throws.

- [ ] **Step 3: Implement the wiring**

In `ContainerService.swift`:

1. Replace the stored properties (lines ~30-34):

```swift
    /// Linux container runtime (Apple `container` or Docker). Nil when neither is present.
    private let linuxBackend: (any LinuxContainerBackend)?
    private let linuxUnavailableMessage: String
```

2. Replace the init params `dockerAvailable`/`dockerUnavailableMessage` (lines ~42-51) with:

```swift
        linuxBackend: (any LinuxContainerBackend)? = nil,
        linuxUnavailableMessage: String =
            "No Linux container runtime found. Install Apple's `container` (recommended) or Docker on the Mac agent.",
```

and in the body: `self.linuxBackend = linuxBackend; self.linuxUnavailableMessage = linuxUnavailableMessage`.

3. In `createContainer`, replace the `if isLinux { throw … }` block (lines 736-741) with registration:

```swift
        if isLinux {
            guard linuxBackend != nil else {
                throw RPCError(code: .failedPrecondition, message: self.linuxUnavailableMessage)
            }
            try await self.registerApp(
                id: appName,
                kind: .container,
                container: WendyApp.ContainerMetadata(imageName: imageName, appConfig: appConfig)
            )
            logger.info("Registered Linux container app", metadata: ["app_name": "\(appName)", "image": "\(imageName)"])
            return ServerResponse(message: Wendy_Agent_Services_V1_CreateContainerResponse())
        }
```

4. In `startContainer`, replace the `if app.container != nil { throw … }` block (lines 868-873) with a backend launch that reuses the streaming return. Extract the streaming closure (lines 947-1002) into a helper `makeStreamingResponse(appName:process:stdoutPipe:stderrPipe:)` so both native and container paths share it, then:

```swift
        if let container = app.container {
            guard let linuxBackend else {
                throw RPCError(code: .failedPrecondition, message: self.linuxUnavailableMessage)
            }
            try await linuxBackend.pull(image: container.imageName)
            let launchToken = UUID()
            self.prepareAppForLaunch(id: appName, launchToken: launchToken)
            let (process, stdoutPipe, stderrPipe) = try await linuxBackend.createAndStart(
                appName: appName,
                imageName: container.imageName,
                appConfig: container.appConfig,
                terminationHandler: self.makeTerminationHandler(forAppID: appName, launchToken: launchToken)
            )
            try await self.markAppRunning(id: appName, process: process, launchToken: launchToken)
            logger.info("Container started", metadata: ["app_name": "\(appName)", "pid": "\(process.processIdentifier)"])
            return self.makeStreamingResponse(
                appName: appName, process: process, stdoutPipe: stdoutPipe, stderrPipe: stderrPipe
            )
        }
```

5. In `stopTrackedAppIfRunning` (line 314), change `let dockerBackend` → `let linuxBackend` and call `try await linuxBackend.stop(appName: id)`.

6. In the delete path (line ~1039-1040), change `let dockerBackend` → `let linuxBackend` and call `try await linuxBackend.remove(appName: appName)`.

7. Remove the now-unused `dockerUnavailableMessage`/`linuxContainersUnsupportedMessage` fields and any references.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd swift/WendyAgentCore && swift test --filter ContainerServiceLinuxTests && swift build`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Services/ContainerService.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/ContainerServiceLinuxTests.swift
git commit -m "feat(mac): run Linux containers in ContainerService via LinuxContainerBackend"
```

---

### Task 7: Backend selection + registry startup in `WendyAgent`

Replace the hardcoded-disabled `prepareDockerIfNeeded` with real runtime probing (prefer `container`, else `docker`), start the embedded registry, and pass the chosen backend into `ContainerService`.

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/WendyAgent.swift` (190-226; the call site that invokes `prepareDockerIfNeeded`/`startMainServer`)
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/BackendSelectionTests.swift`

**Interfaces:**
- Consumes: `ContainerCLI`, `DockerCLI`, `ContainerCLIBackend`, `DockerContainerBackend`, `AgentImageRegistry`, `BlobStore`.
- Produces: `enum LinuxRuntimeSelector { static func choose(containerAvailable: Bool, dockerAvailable: Bool) -> LinuxRuntimeKind? }` where `enum LinuxRuntimeKind { case appleContainer, docker }`; a private `makeLinuxBackend()` on the agent that probes and constructs the concrete backend; registry started as a stored background `Task`.

- [ ] **Step 1: Write the failing test**

```swift
// swift/WendyAgentCore/Tests/WendyAgentTests/BackendSelectionTests.swift
import Testing

@testable import WendyAgentCore

@Suite struct BackendSelectionTests {
    @Test func prefersAppleContainerWhenBothAvailable() {
        #expect(LinuxRuntimeSelector.choose(containerAvailable: true, dockerAvailable: true) == .appleContainer)
    }
    @Test func fallsBackToDocker() {
        #expect(LinuxRuntimeSelector.choose(containerAvailable: false, dockerAvailable: true) == .docker)
    }
    @Test func nilWhenNeitherAvailable() {
        #expect(LinuxRuntimeSelector.choose(containerAvailable: false, dockerAvailable: false) == nil)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter BackendSelectionTests`
Expected: FAIL — `LinuxRuntimeSelector` not defined.

- [ ] **Step 3: Implement selection + wiring**

Add the selector (in `WendyAgent.swift` or a small new `Containers/LinuxRuntimeSelector.swift`):

```swift
enum LinuxRuntimeKind: Equatable, Sendable { case appleContainer, docker }

enum LinuxRuntimeSelector {
    /// Prefer Apple's `container`; fall back to Docker; nil if neither present.
    static func choose(containerAvailable: Bool, dockerAvailable: Bool) -> LinuxRuntimeKind? {
        if containerAvailable { return .appleContainer }
        if dockerAvailable { return .docker }
        return nil
    }
}
```

Replace `prepareDockerIfNeeded()` with a probe that returns a constructed backend:

```swift
    private func makeLinuxBackend() async -> (any LinuxContainerBackend)? {
        let containerAvailable = await ContainerCLI().checkAvailable()
        let dockerAvailable = await DockerCLI().checkAvailable()
        switch LinuxRuntimeSelector.choose(
            containerAvailable: containerAvailable, dockerAvailable: dockerAvailable
        ) {
        case .appleContainer:
            self.logger.info("Linux container runtime: Apple container")
            return ContainerCLIBackend()
        case .docker:
            self.logger.info("Linux container runtime: Docker")
            return DockerContainerBackend()
        case nil:
            self.logger.info("No Linux container runtime available; native macOS apps only")
            return nil
        }
    }
```

In `startMainServer`, replace the `dockerAvailability`-based construction (lines 204-224). The method currently takes `dockerAvailability:`; change its signature to take `linuxBackend: (any LinuxContainerBackend)?` and pass it straight through:

```swift
        let containerService = ContainerService(
            broadcaster: broadcaster,
            executablePath: self.configuration.appPath,
            sandboxProfilePath: self.configuration.sandboxProfile.isEmpty ? nil : self.configuration.sandboxProfile,
            stateDirectory: stateDirectory,
            appsBase: appsBase,
            linuxBackend: linuxBackend,
            onAppsChanged: { [weak self] apps in await self?.updateApps(apps) }
        )
```

Start the registry once (only when a backend exists), storing the task so it can be cancelled on shutdown. Add a stored `private var registryTask: Task<Void, Never>?` and, right after constructing `containerService` when `linuxBackend != nil`:

```swift
        if linuxBackend != nil, self.registryTask == nil {
            let registry = AgentImageRegistry(store: BlobStore(root: stateDirectory))
            self.registryTask = Task {
                do { try await registry.run() }
                catch { self.logger.error("Registry stopped", metadata: ["error": "\(error)"]) }
            }
        }
```

Update the caller of `startMainServer` to first `let backend = await makeLinuxBackend()` and pass `linuxBackend: backend`. Delete `prepareDockerIfNeeded` and the `Self.linuxContainersUnsupportedMessage` constant (lines 190-202). Cancel `registryTask` wherever `stopMainServer()` tears down (add `self.registryTask?.cancel(); self.registryTask = nil`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd swift/WendyAgentCore && swift test --filter BackendSelectionTests && swift build`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/WendyAgent.swift \
        swift/WendyAgentCore/Sources/WendyAgent/Containers/ \
        swift/WendyAgentCore/Tests/WendyAgentTests/BackendSelectionTests.swift
git commit -m "feat(mac): select Linux runtime (container>docker) and start embedded registry"
```

---

### Task 8: Lift the CLI darwin-linux block

Allow Linux/container projects to target a darwin agent. Native-darwin behavior and genuinely-unsupported platforms (e.g. windows) still report the platform mismatch.

**Files:**
- Modify: `go/internal/cli/commands/run.go` (const at 43; `rejectUnsupportedMacRunProject` 49-60; keep call sites 796, 1388)
- Test: `go/internal/cli/commands/run_macos_test.go`

**Interfaces:**
- Produces: `rejectUnsupportedMacRunProject(projectType, platform string) error` returns `nil` for `docker`/`python`/`compose`/`multi-service`/`swift`/`xcode` when the resolved platform OS is `darwin` (native) OR `linux`/`wendyos` (now supported via the Mac agent's container runtime). It still rejects a truly foreign platform (e.g. `windows`).

- [ ] **Step 1: Update the failing test**

Open `run_macos_test.go`, find the case asserting that a `docker`/`linux` project against a darwin target returns `macContainersUnsupportedMessage`, and invert it:

```go
func TestRejectUnsupportedMacRunProject_AllowsLinuxContainers(t *testing.T) {
    // Linux container projects are now supported on the Mac agent.
    for _, pt := range []string{"docker", "python", "compose", "multi-service"} {
        if err := rejectUnsupportedMacRunProject(pt, "linux/arm64"); err != nil {
            t.Fatalf("projectType %q on linux/arm64 should be allowed, got: %v", pt, err)
        }
    }
    // Native darwin still fine.
    if err := rejectUnsupportedMacRunProject("swift", "darwin"); err != nil {
        t.Fatalf("swift/darwin should be allowed, got: %v", err)
    }
    // A genuinely unsupported platform still rejected.
    if err := rejectUnsupportedMacRunProject("docker", "windows/amd64"); err == nil {
        t.Fatalf("windows target should still be rejected")
    }
}
```

Remove/replace any existing test that asserts the old rejection (search for `macContainersUnsupportedMessage` in the test file).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestRejectUnsupportedMacRunProject`
Expected: FAIL — current code returns the unsupported message for linux docker projects.

- [ ] **Step 3: Update `rejectUnsupportedMacRunProject`**

```go
func rejectUnsupportedMacRunProject(projectType, platform string) error {
    osName := platformOS(platform)
    // Native darwin apps and Linux/WendyOS containers (via the Mac agent's
    // container runtime) are both supported. Anything else is a real mismatch.
    if !strings.EqualFold(osName, appconfig.PlatformDarwin) &&
        !strings.EqualFold(osName, "linux") &&
        !strings.EqualFold(osName, "wendyos") {
        return errors.New(macPlatformMismatchMessage(platform))
    }
    switch projectType {
    case "swift", "xcode", "docker", "python", "compose", "multi-service":
        return nil
    default:
        return fmt.Errorf("unable to detect project type for a Mac target: %q", projectType)
    }
}
```

Delete the now-unused `macContainersUnsupportedMessage` const (line 43) and any remaining reference to it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/ -run TestRejectUnsupportedMacRunProject && go build ./...`
Expected: PASS; build clean (fix any leftover references to the deleted const).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/run.go go/internal/cli/commands/run_macos_test.go
git commit -m "feat(cli): allow Linux container projects to target the Mac agent"
```

---

### Task 9: End-to-end verification, docs, and formatting

Tie it together: document the manual E2E (hardware-gated, per prior Mac-agent PRs), run the full suites, and swift-format.

**Files:**
- Create: `swift/WendyE2ETests/` note or append to the existing Mac E2E docs (follow the pattern of prior Mac-agent E2E notes; if none, add a short section to `specs/2026-07-11-mac-agent-linux-containers-design.md` under a new "## Manual E2E" heading).
- Modify: any files flagged by `swift-format`.

- [ ] **Step 1: Full Swift test suite**

Run: `cd swift/WendyAgentCore && swift test`
Expected: All tests pass (existing + the new suites from Tasks 1-7).

- [ ] **Step 2: Full Go test suite**

Run: `cd go && go test ./...`
Expected: All pass.

- [ ] **Step 3: swift-format**

Run: `swift/Scripts/Lint.sh` (or `swift format --in-place --recursive swift/WendyAgentCore/Sources swift/WendyAgentCore/Tests` per `.swift-format`).
Expected: No diffs after a second run.

- [ ] **Step 4: Document the manual E2E**

Write the manual procedure (to be run on a full box with Xcode + `container`):

```
1. Build & launch WendyAgentMac; confirm log "Linux container runtime: Apple container"
   and "Agent image registry listening port=5555".
2. In a Linux/arm64 sample with a Dockerfile (e.g. Examples/*), run:
     wendy run --device "<Mac agent>"
   Confirm: CLI builds + pushes to localhost:5555; agent pulls; container starts;
   stdout/stderr stream back in the CLI.
3. `container list --all` shows `wendy-<app>` with label wendy.managed=true.
4. Ctrl-C / `wendy device apps stop <app>`; confirm the container stops and is removed.
5. Repeat with Docker present and `container` absent to exercise the fallback.
```

Add this block under a "## Manual E2E (hardware-gated)" heading in the design doc, and note in the PR body that live enrollment + container run remain unverified on the dev box (no full Xcode / consistent with prior Mac-agent PRs).

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "docs(mac): manual E2E for Linux containers on the Mac agent; swift-format"
```

---

## Self-Review Notes (author)

- **Spec coverage:** §2A→Tasks 1-4; §2B→Task 7; §2C→Task 5; §3 data flow→Tasks 5+6; §4 entitlements→Task 1 (+ rendering in 2/4); §5 CLI→Task 8; §6 error handling→Tasks 6 (unavailable message), 7 (registry bind failure logged), 5 (digest verify); §7 testing→every task + Task 9; §8 out-of-scope untouched.
- **Type consistency:** `LinuxContainerBackend`, `LinuxRunSpec`, `LinuxContainerInfo`, `managedContainerName`, `LinuxRunSpecBuilder.specs(from:appName:warn:)`, `LinuxRuntimeSelector.choose(containerAvailable:dockerAvailable:)`, `ContainerService.init(..., linuxBackend:)` are used with identical signatures across tasks.
- **Known API-confirmation point:** Hummingbird 2 route/response/header spellings in Task 5 (Step 3c) must be checked against the resolved version; the route/status/verification contract is fixed.
