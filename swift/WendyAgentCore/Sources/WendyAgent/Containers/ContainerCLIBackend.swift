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
            from: appConfig?.entitlements ?? [],
            appName: appName,
            warn: warn
        )
    }

    func pull(image: String) async throws {
        // The container system services must be running before a pull/run, and
        // `container --version` (our availability probe) succeeds even when they
        // are stopped. Start them here — idempotent and best-effort: if the
        // start itself fails, let the pull run and surface the real error rather
        // than masking it. This self-heals a Mac where the services were never
        // started ("Plugins are unavailable. … container system start").
        do {
            try await cli.systemStart()
        } catch {
            logger.warning(
                "container system start failed; attempting pull anyway",
                metadata: ["error": "\(error)"]
            )
        }
        logger.info("Pulling image", metadata: ["image": "\(image)"])
        try await cli.pull(image: image)
    }

    func createAndStart(
        appName: String,
        imageName: String,
        appConfig: WendyAppConfig?,
        terminationHandler: (@Sendable (Foundation.Process) -> Void)?
    ) async throws -> (
        process: Foundation.Process,
        stdout: Pipe,
        stderr: Pipe
    ) {
        let name = managedContainerName(for: appName)
        try? await cli.delete(containerName: name)  // clear any stale container
        let specs = Self.specs(for: appConfig, appName: appName) { [logger] message in
            logger.warning("\(message)", metadata: ["app_name": "\(appName)"])
        }
        logger.info(
            "Starting container",
            metadata: [
                "container": "\(name)",
                "image": "\(imageName)",
            ]
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
