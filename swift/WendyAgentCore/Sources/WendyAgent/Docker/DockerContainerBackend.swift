import Foundation
import Logging

/// Runs Linux containers via Docker on a Mac agent.
///
/// When the agent receives a container request with `platform: "linux/..."`, it
/// delegates to this backend. The image is already in the local Docker registry
/// at `localhost:<registryPort>` (pushed by the CLI via the standard buildx pipeline).
actor DockerContainerBackend: LinuxContainerBackend {
    private let docker: DockerCLI
    private let logger = Logger(label: "sh.wendy.agent.docker-backend")

    init(docker: DockerCLI = DockerCLI()) { self.docker = docker }

    /// Render entitlements + managed-container bookkeeping into Docker run options.
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
            from: appConfig?.entitlements ?? [],
            appName: appName,
            warn: warn
        )
        for spec in specs {
            switch spec {
            case .networkNone: options.append(.network("none"))
            case .publishPort(let h, let c): options.append(.publish(hostPort: h, containerPort: c))
            case .volume(let name, let path):
                options.append(.volume(hostOrName: name, containerPath: path))
            }
        }
        return options
    }

    /// Pull an image from the local registry into Docker.
    func pull(image: String) async throws {
        logger.info("Pulling image", metadata: ["image": "\(image)"])
        try await docker.pull(image: image)
    }

    /// Remove any stale container, then create and start a Docker container in
    /// attached mode. Returns the running Process and its stdout/stderr pipes.
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
            options: options,
            image: imageName,
            terminationHandler: terminationHandler
        )
    }

    /// Stop a running Docker container.
    func stop(appName: String) async throws {
        _ = try? await docker.stop(container: managedContainerName(for: appName), timeout: 10)
    }

    /// Remove a Docker container (force).
    func remove(appName: String) async throws {
        _ = try? await docker.rm(options: [.force], container: managedContainerName(for: appName))
    }

    /// List Wendy-managed Docker containers.
    func listContainers() async throws -> [LinuxContainerInfo] {
        try await docker.ps(label: "wendy.managed=true")
    }
}
