import Compression
import ContainerRegistry
import Foundation
import Logging
import Shell

extension ContainerImageSpec {
    private static let logger = Logger(label: "apache-edge.container-builder")

    /// Creates a container image specification with a base image from a registry and an executable to add.
    ///
    /// - Parameters:
    ///   - baseImage: Reference to the base image to use (e.g., "debian:bookworm-slim").
    ///   - executable: The URL of the executable to include in the image.
    ///   - workingDir: The working directory for the container.
    ///   - env: Environment variables to set in the container.
    ///   - created: The date when the image was created.
    ///   - architecture: The target CPU architecture (e.g., "arm64", "amd64").
    ///   - username: Optional username for registry authentication.
    ///   - password: Optional password for registry authentication.
    /// - Returns: A new ContainerImageSpec with base image layers and the executable.
    public static func withBaseImage(
        baseImage: String,
        executable: URL,
        workingDir: String = "/",
        env: [String] = ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],
        created: Date = Date(),
        architecture: String = "arm64",
        username: String? = nil,
        password: String? = nil
    ) async throws -> ContainerImageSpec {
        let imageRef = try ImageReference(fromString: baseImage, defaultRegistry: "docker.io")
        logger.info("Pulling base image: \(imageRef)")

        // Create registry client - for Docker Hub we need to use index.docker.io
        // This automatically handles the redirect for docker.io to index.docker.io
        // Always provide an AuthHandler - use provided credentials if available, or empty auth for anonymous access
        let auth = AuthHandler(username: username, password: password)
        let client = try await RegistryClient(
            registry: imageRef.registry,
            insecure: false,
            auth: auth
        )

        // Try to get manifest for the base image
        // Some registries might return an index instead of a manifest for multi-platform images
        var manifest: ImageManifest
        do {
            manifest = try await client.getManifest(
                repository: imageRef.repository,
                reference: imageRef.reference
            )
        } catch {
            // If fetching manifest fails, try to get the index and select the appropriate manifest
            logger.debug("Direct manifest fetch failed, trying to get image index...")
            let index = try await client.getIndex(
                repository: imageRef.repository,
                reference: imageRef.reference
            )

            // Find the manifest for the requested architecture
            guard
                let manifestDesc = index.manifests.first(where: {
                    $0.platform?.architecture == architecture && $0.platform?.os == "linux"
                }) ?? index.manifests.first
            else {
                throw error  // Re-throw the original error if no suitable manifest found
            }

            logger.info(
                "Using manifest for \(manifestDesc.platform?.architecture ?? "unknown") architecture"
            )

            // Now fetch the specific manifest using the digest
            manifest = try await client.getManifest(
                repository: imageRef.repository,
                reference: manifestDesc.digest
            )
        }

        // Get configuration for the base image
        let configDigest = manifest.config.digest
        let config = try await client.getImageConfiguration(
            forImage: imageRef,
            digest: configDigest
        )

        // Create a temporary directory to store the base image layers
        let tempDir = FileManager.default.temporaryDirectory.appendingPathComponent(
            UUID().uuidString
        )
        try FileManager.default.createDirectory(at: tempDir, withIntermediateDirectories: true)

        // Download each layer and store as tarball layer
        var baseLayers: [Layer] = []

        // We need to maintain the original diffIDs from the config
        let originalDiffIDs = config.rootfs.diff_ids
        if originalDiffIDs.count != manifest.layers.count {
            logger.warning(
                "Number of layers in manifest (\(manifest.layers.count)) doesn't match diffIDs in config (\(originalDiffIDs.count))"
            )
        }

        for (index, layer) in manifest.layers.enumerated() {
            logger.info("Downloading layer \(index+1)/\(manifest.layers.count): \(layer.digest)")
            let layerData = try await client.getBlob(
                repository: imageRef.repository,
                digest: layer.digest
            )

            // Save layer to temporary file
            let layerFilename = layer.digest.replacingOccurrences(of: ":", with: "-")
            let layerPath = tempDir.appendingPathComponent(layerFilename)
            try layerData.write(to: layerPath)

            // Use the tarball directly as a layer with the original diffID
            if index < originalDiffIDs.count {
                let diffID = originalDiffIDs[index]
                logger.debug("Using original diffID for layer \(index+1): \(diffID)")
                baseLayers.append(Layer(tarball: layerPath, diffID: diffID))
            } else {
                baseLayers.append(Layer(tarball: layerPath))
            }
        }

        // Create executable layer
        let executableName = executable.lastPathComponent
        let executableFiles = [
            Layer.File(
                source: executable,
                destination: "/bin/\(executableName.lowercased())",
                permissions: 0o755
            )
        ]
        let executableLayer = Layer(files: executableFiles)

        // Combine base layers with the executable layer
        var allLayers = baseLayers
        allLayers.append(executableLayer)

        // Clean up temporary directory on exit
        // We don't delete here because the tarballs are still needed
        // The caller is responsible for cleanup or the OS will clean up temp files

        // Create container image spec with base image configuration
        return ContainerImageSpec(
            architecture: config.architecture,
            os: config.os,
            cmd: ["/bin/\(executableName.lowercased())"],
            env: env,
            workingDir: workingDir,
            layers: allLayers,
            created: created
        )
    }
}
