import AppConfig
import ContainerRegistry
import ContainerdGRPC
import EdgeAgentGRPC
import EdgeShared
import Foundation
import Logging
import NIOCore
import NIOFoundationCompat
import _NIOFileSystem

struct EdgeContainerService: Edge_Agent_Services_V1_EdgeContainerService.ServiceProtocol {
    let logger = Logger(label: "EdgeContainerService")

    func listLayers(
        request: ServerRequest<Edge_Agent_Services_V1_ListLayersRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Edge_Agent_Services_V1_LayerHeader> {
        return StreamingServerResponse { writer in
            try await Containerd.withClient { client in
                try await client.listContent { items in
                    try await writer.write(
                        contentsOf: items.map { item in
                            Edge_Agent_Services_V1_LayerHeader.with { header in
                                header.digest = item.digest
                                header.size = item.size
                            }
                        }
                    )
                }
            }

            return Metadata()
        }
    }
    func listContainers(
        request: GRPCCore.ServerRequest<Edge_Agent_Services_V1_ListContainersRequest>,
        context: GRPCCore.ServerContext
    ) async throws -> GRPCCore.StreamingServerResponse<Edge_Agent_Services_V1_ListContainersResponse> {
        return StreamingServerResponse { writer in
            try await Containerd.withClient { client in
                let tasks = try await client.listTasks()
                let containers = try await client.listContainers()

                for container in containers {
                    try await writer.write(.with { 
                        $0.container.appName = container.id
                        $0.container.appVersion = container.labels["sh.wendy/app.version"] ?? "0.0.0"
                        
                        if 
                            let restartCount = container.labels["containerd.io/restart.count"],
                            let restartCount = UInt32(restartCount) 
                        {
                            $0.container.failureCount = restartCount
                        }
                        
                        if let task: Containerd_V1_Types_Process = tasks.first(where: { $0.id == container.id }) {
                            $0.container.runningState = task.status == .running ? .running : .stopped
                        } else {
                            $0.container.runningState = .stopped
                        }
                    })
                }
            }

            return Metadata()
        }
    }

    func writeLayer(
        request: StreamingServerRequest<Edge_Agent_Services_V1_WriteLayerRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Edge_Agent_Services_V1_WriteLayerResponse> {
        return StreamingServerResponse { writer in
            nonisolated(unsafe) var iterator = request.messages.makeAsyncIterator()
            guard let firstChunk = try await iterator.next() else {
                throw RPCError(code: .aborted, message: "No initial chunk provided.")
            }

            try await Containerd.withClient { client in
                try await client.writeLayer(ref: firstChunk.digest) { writer in
                    try await writer.write(data: firstChunk.data)

                    while let nextChunk = try await iterator.next() {
                        try await writer.write(data: nextChunk.data)
                    }
                }
            }

            return Metadata()
        }
    }

    func runContainer(
        request: ServerRequest<Edge_Agent_Services_V1_RunContainerLayersRequest>,
        context: ServerContext
    ) async throws -> ServerResponse<Edge_Agent_Services_V1_RunContainerLayersResponse> {
        try await Containerd.withClient { client in
            do {
                let request = request.message
                var labels = [String: String]()

                do {
                    let restartPolicy = request.restartPolicy
                    let restartPolicyLabel = "containerd.io/restart.policy"

                    switch restartPolicy.mode {
                    case .default, .unlessStopped, .UNRECOGNIZED:
                        labels[restartPolicyLabel] = "unless-stopped"
                    case .no:
                        labels[restartPolicyLabel] = "no"
                    case .onFailure:
                        labels[restartPolicyLabel] =
                            "on-failure:\(restartPolicy.onFailureMaxRetries)"
                    }
                }

                async let killed: Void = try await client.stopTask(containerID: request.appName)

                logger.info("Creating container config.json")
                let config = ImageConfiguration(
                    architecture: "arm64",
                    os: "linux",
                    config: ImageConfigurationConfig(
                        Cmd: request.cmd.split(separator: " ").map(String.init),
                        StopSignal: "SIGTERM"
                    ),
                    rootfs: ImageConfigurationRootFS(diff_ids: request.layers.map(\.diffID))
                )
                let (configHash, configSize) = try await client.uploadJSON(config)

                logger.debug("Creating container manifest")
                let manifest = ImageManifest(
                    mediaType: "application/vnd.oci.image.manifest.v1+json",
                    config: ContentDescriptor(
                        mediaType: "application/vnd.oci.image.config.v1+json",
                        digest: "sha256:\(configHash)",
                        size: configSize
                    ),
                    layers: request.layers.map { layer in
                        return ContentDescriptor(
                            mediaType: layer.gzip
                                ? "application/vnd.oci.image.layer.v1.tar+gzip"
                                : "application/vnd.oci.image.layer.v1.tar",
                            digest: layer.digest,
                            size: layer.size
                        )
                    }
                )
                let (manifestHash, manifestSize) = try await client.uploadJSON(manifest)

                do {
                    logger.info("Creating image \(request.imageName)")
                    try await client.createImage(
                        named: request.imageName,
                        manifestHash: manifestHash,
                        manifestSize: manifestSize
                    )
                } catch {
                    try await client.updateImage(
                        named: request.imageName,
                        manifestHash: manifestHash,
                        manifestSize: manifestSize
                    )
                }

                let appConfig: AppConfig

                if request.appConfig.isEmpty {
                    appConfig = AppConfig(
                        appId: request.appName,
                        version: "0.0.0",
                        entitlements: []
                    )
                } else {
                    appConfig = try JSONDecoder().decode(AppConfig.self, from: request.appConfig)
                }

                labels["sh.wendy/app.version"] = appConfig.version

                var spec = OCI(
                    process: .init(
                        user: .root,
                        args: request.cmd.split(separator: " ").map(String.init),
                        env: [
                            "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
                        ],
                        cwd: request.workingDir.isEmpty ? "/" : request.workingDir
                    ),
                    root: .init(path: "rootfs", readonly: false),
                    hostname: request.appName,
                    mounts: [
                        .init(destination: "/proc", type: "proc", source: "proc"),
                        // Needed for TTY support (requirement for DS2)
                        .init(
                            destination: "/dev/pts",
                            type: "devpts",
                            source: "devpts",
                            options: [
                                "nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620",
                            ]
                        ),
                        .init(
                            destination: "/dev/shm",
                            type: "tmpfs",
                            source: "shm",
                            options: ["nosuid", "noexec", "nodev", "mode=1777", "size=65536k"]
                        ),
                        .init(
                            destination: "/dev/mqueue",
                            type: "mqueue",
                            source: "mqueue",
                            options: ["nosuid", "noexec", "nodev"]
                        ),
                    ],
                    linux: .init(
                        namespaces: [
                            .init(type: "pid"),
                            .init(type: "ipc"),
                            .init(type: "uts"),
                            .init(type: "mount"),
                        ],
                        networkMode: "host",
                        capabilities: .init(
                            bounding: ["SYS_PTRACE"],
                            effective: ["SYS_PTRACE"],
                            inheritable: ["SYS_PTRACE"],
                            permitted: ["SYS_PTRACE"],
                        ),
                        seccomp: Seccomp(
                            defaultAction: "SCMP_ACT_ALLOW",
                            architectures: ["SCMP_ARCH_AARCH64"],
                            syscalls: []
                        ),
                        devices: []
                    )
                )

                spec.applyEntitlements(
                    entitlements: appConfig.entitlements,
                    appName: request.appName
                )

                let snapshotKey: String?
                let mounts: [Containerd_Types_Mount]

                do {
                    (snapshotKey, mounts) = try await client.createSnapshot(
                        imageName: request.imageName,
                        appName: request.appName,
                        layers: request.layers
                    )
                } catch let error as RPCError {
                    logger.error(
                        "Failed to create snapshot",
                        metadata: [
                            "error": .stringConvertible(error.description)
                        ]
                    )
                    throw error
                }

                do {
                    logger.info("Creating container \(request.appName) from \(request.imageName)")
                    try await client.createContainer(
                        imageName: request.imageName,
                        appName: request.appName,
                        snapshotKey: snapshotKey ?? "",
                        ociSpec: try JSONEncoder().encode(spec),
                        labels: labels
                    )
                } catch let error as RPCError where error.code == .alreadyExists {
                    logger.debug("Container already exists, updating container")
                    try await client.updateContainer(
                        imageName: request.imageName,
                        appName: request.appName,
                        snapshotKey: snapshotKey ?? "",
                        ociSpec: try JSONEncoder().encode(spec)
                    )
                }

                do {
                    try await killed
                    logger.info(
                        "Killed running container",
                        metadata: [
                            "container-id": .stringConvertible(request.appName)
                        ]
                    )
                    try await client.deleteTask(containerID: request.appName)
                } catch let error as RPCError where error.code == .notFound {
                    logger.info("Container wasn't running")
                } catch let error as RPCError {
                    logger.error(
                        "Failed to kill container",
                        metadata: [
                            "container-id": .stringConvertible(request.appName),
                            "error": .stringConvertible(error.description),
                        ]
                    )
                    throw error
                } catch {
                    logger.error(
                        "Failed to kill container",
                        metadata: [
                            "container-id": .stringConvertible(request.appName),
                            "error": .stringConvertible(error.localizedDescription),
                        ]
                    )
                    throw error
                }

                logger.info("Creating task")
                do {
                    try await client.createTask(
                        containerID: request.appName,
                        appName: request.appName,
                        snapshotName: snapshotKey ?? "",
                        mounts: mounts
                    )
                } catch let error as RPCError where error.code == .alreadyExists {
                    logger.info(
                        "Task already exists, re-creating it",
                        metadata: [
                            "container-id": .stringConvertible(request.appName)
                        ]
                    )
                    try await client.deleteTask(containerID: request.appName)
                    logger.debug(
                        "Task removed",
                        metadata: [
                            "container-id": .stringConvertible(request.appName)
                        ]
                    )
                    try await client.createTask(
                        containerID: request.appName,
                        appName: request.appName,
                        snapshotName: snapshotKey ?? "",
                        mounts: mounts
                    )
                    logger.debug(
                        "Task created",
                        metadata: [
                            "container-id": .stringConvertible(request.appName)
                        ]
                    )
                }

                logger.info("Starting task")
                try await client.runTask(containerID: request.appName)

                return ServerResponse(
                    message: .init()
                )
            } catch let error as RPCError {
                logger.error(
                    "Failed to run container",
                    metadata: [
                        "error": .stringConvertible(error.description)
                    ]
                )
                throw error
            } catch {
                logger.error(
                    "Failed to run container",
                    metadata: [
                        "error": .stringConvertible(error.localizedDescription)
                    ]
                )
                throw error
            }
        }
    }

    func stopContainer(
        request: ServerRequest<Edge_Agent_Services_V1_StopContainerRequest>,
        context: ServerContext
    ) async throws -> ServerResponse<Edge_Agent_Services_V1_StopContainerResponse> {
        try await Containerd.withClient { client in
            let appName = request.message.appName
            logger.info(
                "Stopping container",
                metadata: ["container-id": .stringConvertible(appName)]
            )
            do {
                try await client.stopTask(containerID: appName)
                logger.info(
                    "Stopped container",
                    metadata: ["container-id": .stringConvertible(appName)]
                )
            } catch let error as RPCError where error.code == .notFound {
                logger.info(
                    "Container wasn't running",
                    metadata: ["container-id": .stringConvertible(appName)]
                )
            } catch let error as RPCError {
                logger.error(
                    "Failed to stop container",
                    metadata: [
                        "container-id": .stringConvertible(appName),
                        "error": .stringConvertible(error.description),
                    ]
                )
                throw error
            }

            return ServerResponse(message: .init())
        }
    }
}
