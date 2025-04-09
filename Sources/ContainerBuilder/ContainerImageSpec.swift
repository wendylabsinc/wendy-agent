import Foundation

/// Represents a container image specification that can be used to build an image.
public struct ContainerImageSpec {
    /// The CPU architecture for which the image is built.
    public var architecture: String

    /// The operating system for which the image is built.
    public var os: String

    /// The command to run when the container starts.
    public var cmd: [String]

    /// The environment variables for the container.
    public var env: [String]

    /// The working directory for the container.
    public var workingDir: String

    /// The layers that make up the container image.
    public var layers: [Layer]

    /// The date when the image was created.
    public var created: Date

    /// Creates a new container image specification.
    ///
    /// - Parameters:
    ///   - architecture: The CPU architecture for which the image is built (default: "arm64").
    ///   - os: The operating system for which the image is built (default: "linux").
    ///   - cmd: The command to run when the container starts.
    ///   - env: The environment variables for the container.
    ///   - workingDir: The working directory for the container (default: "/").
    ///   - layers: The layers that make up the container image.
    ///   - created: The date when the image was created (default: current date).
    public init(
        architecture: String = "arm64",
        os: String = "linux",
        cmd: [String],
        env: [String] = ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],
        workingDir: String = "/",
        layers: [Layer],
        created: Date = Date()
    ) {
        self.architecture = architecture
        self.os = os
        self.cmd = cmd
        self.env = env
        self.workingDir = workingDir
        self.layers = layers
        self.created = created
    }

    /// Creates a container image specification with a single executable.
    ///
    /// - Parameters:
    ///   - architecture: The CPU architecture for which the image is built (default: "arm64").
    ///   - executable: The URL of the executable to include in the image.
    ///   - created: The date when the image was created (default: current date).
    /// - Returns: A new ContainerImageSpec with a single layer containing the executable.
    public static func withExecutable(
        architecture: String = "arm64",
        executable: URL,
        created: Date = Date()
    ) -> ContainerImageSpec {
        let executableName = executable.lastPathComponent
        let containerFiles = [
            Layer.File(
                source: executable,
                destination: "/bin/\(executableName.lowercased())",  // TODO: Lowercasing this is currently a hack to make the executable the same as the image name.
                permissions: 0o755
            )
        ]
        let layer = Layer(files: containerFiles)

        return ContainerImageSpec(
            architecture: architecture,
            cmd: ["/bin/\(executableName)"],
            layers: [layer],
            created: created
        )
    }
}
