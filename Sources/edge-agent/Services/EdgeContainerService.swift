import EdgeAgentGRPC
import EdgeShared
import Foundation
import Logging
import NIOCore
import NIOFoundationCompat
import _NIOFileSystem
import ContainerRegistry

struct EdgeContainerService: Edge_Agent_Services_V1_EdgeContainerService.ServiceProtocol {
    let logger = Logger(label: "EdgeContainerService")

    func listLayers(request: ServerRequest<Edge_Agent_Services_V1_ListLayersRequest>, context: ServerContext) async throws -> StreamingServerResponse<Edge_Agent_Services_V1_LayerHeader> {
        return StreamingServerResponse { writer in
            try await Containerd.withClient { client in
                try await client.listContent { items in
                    try await writer.write(contentsOf: items.map { item in
                        Edge_Agent_Services_V1_LayerHeader.with { header in
                            header.digest = item.digest
                            header.size = item.size
                        }
                    })
                }
            }

            return Metadata()
        }
    }

    func writeLayer(request: StreamingServerRequest<Edge_Agent_Services_V1_WriteLayerRequest>, context: ServerContext) async throws -> StreamingServerResponse<Edge_Agent_Services_V1_WriteLayerResponse> {
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
    
    func runContainer(request: ServerRequest<Edge_Agent_Services_V1_RunContainerLayersRequest>, context: ServerContext) async throws -> ServerResponse<Edge_Agent_Services_V1_RunContainerLayersResponse> {
        try await Containerd.withClient { client in
            let request = request.message

            logger.info("Creating container config.json")
            let config = ImageConfiguration(
                architecture: "arm64",
                os: "linux",
                config: ImageConfigurationConfig(
                    Cmd: [request.cmd],
                    StopSignal: "SIGTERM"
                ),
                rootfs: ImageConfigurationRootFS(diff_ids: request.layers.map(\.diffID))
            )
            let (configHash, configSize) = try await client.uploadJSON(config)

            logger.info("Creating container manifest")
            let manifest = ImageManifest(
                mediaType: "application/vnd.oci.image.manifest.v1+json",
                config: ContentDescriptor(
                    mediaType: "application/vnd.oci.image.config.v1+json",
                    digest: "sha256:\(configHash)",
                    size: configSize
                ),
                layers: request.layers.map { layer in
                    return ContentDescriptor(
                        mediaType: layer.gzip ? "application/vnd.oci.image.layer.v1.tar+gzip" : "application/vnd.oci.image.layer.v1.tar",
                        digest: layer.digest,
                        size: layer.size
                    )
                }
            )
            let (manifestHash, manifestSize) = try await client.uploadJSON(manifest)

            // TODO: Remove or update existing image
            logger.info("Creating image \(request.imageName)")
            try await client.createImage(named: request.imageName, manifestHash: manifestHash, manifestSize: manifestSize)

            // TODO: Replace with a _real_ JSON API like Codable
            let spec = try JSONSerialization.data(withJSONObject: [
                "ociVersion": "1.0.3",
                "process": [
                    "terminal": false,
                    "user": ["uid": 0, "gid": 0],
                    "args": [request.cmd],
                    "env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],
                    "cwd": "/"
                ],
                "root": [
                    "path": "rootfs",
                    "readonly": false
                ],
                "hostname": request.appName,
                "mounts": [
                    [
                        "destination": "/proc",
                        "type": "proc",
                        "source": "proc"
                    ],
                    [
                        "destination": "/dev",
                        "type": "tmpfs",
                        "source": "tmpfs",
                        "options": ["nosuid", "mode=755"]
                    ]
                ],
                "linux": [
                    "namespaces": [
                        ["type": "pid"],
                        ["type": "network"],
                        ["type": "ipc"],
                        ["type": "uts"],
                        ["type": "mount"]
                    ]
                ]
            ])

            // TODO: Remove or update existing container?
            logger.info("Creating container \(request.appName) from \(request.imageName)")
            try await client.createContainer(imageName: request.imageName, appName: request.appName, ociSpec: spec)

            logger.info("Creating task")
            try? await client.createTask(containerID: request.appName, appName: request.appName)

            logger.info("Starting task")
            let execID = try await client.runTask(containerID: request.appName)
            print(execID)

            return ServerResponse(message: .with {
                $0.debugPort = 0
            })
        }
    }
}