import Foundation
import Logging

/// Manages the lifecycle of Linux containers running via Docker on a Mac agent.
///
/// When the agent receives a container request with `platform: "linux/..."`, it
/// delegates to this backend. The image is already in the local Docker registry
/// at `localhost:<registryPort>` (pushed by the CLI via the standard buildx pipeline).
actor DockerContainerBackend {
    enum FileSyncMountError: Error, CustomStringConvertible {
        case invalidDestination(String)
        case missingSource(String)

        var description: String {
            switch self {
            case .invalidDestination(let path):
                "Invalid file sync destination: \(path)"
            case .missingSource(let path):
                "File sync source does not exist: \(path)"
            }
        }
    }

    private let docker = DockerCLI()
    private let logger = Logger(label: "sh.wendy.agent.docker-backend")

    /// Pull an image from the local registry into Docker.
    func pullImage(_ imageName: String) async throws {
        logger.info("Pulling image", metadata: ["image": "\(imageName)"])
        try await docker.pull(image: imageName)
    }

    /// Remove any stale container, then create and start a Docker container in
    /// attached mode. Returns the running Process and its stdout/stderr pipes.
    func createAndStart(
        appName: String,
        imageName: String,
        appConfig: WendyAppConfig?,
        appDirectory: URL,
        workingDir explicitWorkingDir: String?,
        terminationHandler: (@Sendable (Foundation.Process) -> Void)? = nil
    ) async throws -> (process: Foundation.Process, stdout: Pipe, stderr: Pipe) {
        let containerName = "wendy-\(appName)"

        // Remove any stale container with the same name.
        _ = try? await docker.rm(options: [.force], container: containerName)

        var options: [DockerCLI.RunOption] = [
            .rm,
            .name(containerName),
            .label(key: "wendy.managed", value: "true"),
            .label(key: "wendy.app-name", value: appName),
        ]

        // Map entitlements to Docker flags.
        if let entitlements = appConfig?.entitlements {
            options += dockerOptions(from: entitlements, appName: appName)
        }

        let workingDir: String
        if let explicitWorkingDir {
            workingDir = explicitWorkingDir
            options.append(.workdir(explicitWorkingDir))
        } else {
            workingDir = try await docker.imageWorkingDirectory(image: imageName) ?? "/"
        }
        options += try fileSyncMountOptions(
            from: appConfig?.files ?? [],
            appDirectory: appDirectory,
            workingDir: workingDir
        )

        logger.info(
            "Starting Docker container",
            metadata: [
                "container": "\(containerName)",
                "image": "\(imageName)",
            ]
        )

        return try docker.runAttached(
            options: options,
            image: imageName,
            terminationHandler: terminationHandler
        )
    }

    /// Stop a running Docker container.
    func stop(appName: String) async throws {
        let containerName = "wendy-\(appName)"
        logger.info("Stopping Docker container", metadata: ["container": "\(containerName)"])
        _ = try? await docker.stop(container: containerName, timeout: 10)
    }

    /// Remove a Docker container (force).
    func remove(appName: String) async throws {
        let containerName = "wendy-\(appName)"
        logger.info("Removing Docker container", metadata: ["container": "\(containerName)"])
        _ = try? await docker.rm(options: [.force], container: containerName)
    }

    /// List Wendy-managed Docker containers.
    func listContainers() async throws -> [DockerCLI.ContainerInfo] {
        try await docker.ps(label: "wendy.managed=true")
    }

    // MARK: - File sync mounts

    func fileSyncMountOptionsForTesting(
        from files: [WendyFileSyncConfigEntry],
        appDirectory: URL,
        workingDir: String
    ) throws -> [DockerCLI.RunOption] {
        try fileSyncMountOptions(from: files, appDirectory: appDirectory, workingDir: workingDir)
    }

    private func fileSyncMountOptions(
        from files: [WendyFileSyncConfigEntry],
        appDirectory: URL,
        workingDir: String
    ) throws -> [DockerCLI.RunOption] {
        try files.map { entry in
            let destinationRelativePath = try cleanFileSyncRelativePath(
                entry.to ?? stripLeadingDotSlash(entry.path)
            )
            let sourceURL = appDirectory.appendingPathComponent(destinationRelativePath)
            guard FileManager.default.fileExists(atPath: sourceURL.path) else {
                throw FileSyncMountError.missingSource(sourceURL.path)
            }
            return .bindReadOnly(
                source: sourceURL.path,
                target: posixJoin(workingDir, destinationRelativePath)
            )
        }
    }

    private func stripLeadingDotSlash(_ path: String) -> String {
        path.hasPrefix("./") ? String(path.dropFirst(2)) : path
    }

    private func cleanFileSyncRelativePath(_ path: String) throws -> String {
        guard !path.isEmpty, !path.hasPrefix("/") else {
            throw FileSyncMountError.invalidDestination(path)
        }
        let components = path.split(separator: "/").map(String.init)
        guard !components.contains("."), !components.contains("..") else {
            throw FileSyncMountError.invalidDestination(path)
        }
        return components.joined(separator: "/")
    }

    private func posixJoin(_ base: String, _ relativePath: String) -> String {
        let cleanBase = cleanAbsolutePOSIXPath(base)
        if cleanBase == "/" {
            return "/" + relativePath
        }
        return cleanBase + "/" + relativePath
    }

    private func cleanAbsolutePOSIXPath(_ path: String) -> String {
        let absolutePath = "/" + path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        var components: [String] = []
        for component in absolutePath.split(separator: "/") {
            switch component {
            case ".":
                continue
            case "..":
                if !components.isEmpty {
                    components.removeLast()
                }
            default:
                components.append(String(component))
            }
        }
        return components.isEmpty ? "/" : "/" + components.joined(separator: "/")
    }

    // MARK: - Entitlement mapping

    /// Translate Wendy entitlements into Docker run options.
    private func dockerOptions(
        from entitlements: [WendyEntitlement],
        appName: String
    ) -> [DockerCLI.RunOption] {
        var options: [DockerCLI.RunOption] = []

        for entitlement in entitlements {
            switch entitlement.type {
            case "network":
                if entitlement.mode == "none" {
                    options.append(.network("none"))
                } else {
                    // --network=host doesn't work on Docker Desktop for Mac.
                    // Map explicit ports from the entitlement's ports array.
                    if let ports = entitlement.ports {
                        for port in ports {
                            options.append(
                                .publish(hostPort: port.host, containerPort: port.container)
                            )
                        }
                    }
                }

            case "persist":
                if let name = entitlement.name, let path = entitlement.path {
                    let volumeName = "wendy-\(appName)-\(name)"
                    options.append(.volume(hostOrName: volumeName, containerPath: path))
                }

            case "gpu", "bluetooth", "audio", "video", "camera", "usb", "i2c", "gpio":
                logger.warning(
                    "Entitlement '\(entitlement.type)' is not available for Linux containers on macOS (VM isolation)",
                    metadata: ["app_name": "\(appName)"]
                )

            default:
                logger.warning(
                    "Unknown entitlement type '\(entitlement.type)'",
                    metadata: ["app_name": "\(appName)"]
                )
            }
        }

        return options
    }
}
