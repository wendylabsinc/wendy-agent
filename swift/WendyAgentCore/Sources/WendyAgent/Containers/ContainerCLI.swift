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

    /// Start Apple's container system services (`container system start`). The
    /// runtime needs these running before any pull/run; `container --version`
    /// (used by `checkAvailable`) succeeds even when they are stopped, so this
    /// must be invoked explicitly. Idempotent: a no-op when already running.
    /// Booting the backing services/VM can take a while, hence the long timeout.
    func systemStart() async throws {
        _ = try await run(["system", "start"], timeout: .seconds(120))
    }

    // MARK: - Image + lifecycle

    func pull(image: String) async throws {
        // `container` has no top-level `pull`; the subcommand is `image pull`.
        _ = try await run(["image", "pull", "--scheme", "http", image], timeout: .seconds(600))
    }

    func runAttached(
        containerName: String,
        imageName: String,
        specs: [LinuxRunSpec],
        env: [String: String],
        terminationHandler: (@Sendable (Foundation.Process) -> Void)?
    ) throws -> (process: Foundation.Process, stdout: Pipe, stderr: Pipe) {
        let args = Self.runArguments(
            containerName: containerName,
            imageName: imageName,
            specs: specs,
            env: env
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
            let labels =
                (config["labels"] as? [String: Any]) ?? (entry["labels"] as? [String: Any]) ?? [:]
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
                executable: executable,
                searchedPaths: resolution.searchedPaths
            )
        }
        return path
    }

    /// Run a short `container` command via the shared `Subprocess` helper;
    /// throw on nonzero exit. Long-running attached runs use `runAttached`.
    /// Note: the struct's stored `environment` is used only for PATH resolution,
    /// not passed to the subprocess (unlike `runAttached`).
    @discardableResult
    private func run(_ arguments: [String], timeout: Duration = .seconds(30)) async throws -> String
    {
        let resolved = try resolvedExecutablePath()
        let result = try await Subprocess.run(resolved, arguments, timeout: timeout)
        guard result.status == 0 else {
            logger.warning(
                "container command failed",
                metadata: [
                    "args": .array(arguments.map { .stringConvertible($0) }),
                    "status": .stringConvertible(result.status),
                ]
            )
            throw ContainerCLIError.commandFailed(
                executable: resolved,
                args: arguments,
                status: result.status,
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
            return
                "Could not find \(executable). Looked in: \(searchedPaths.joined(separator: ", "))"
        case .commandFailed(let executable, let args, let status, let stderr):
            let cmd = ([executable] + args).joined(separator: " ")
            return stderr.isEmpty
                ? "\(cmd) exited with status \(status)"
                : "\(cmd) exited with status \(status): \(stderr)"
        }
    }
}
