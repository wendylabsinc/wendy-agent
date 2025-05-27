import GRPCCore
import GRPCNIOTransportHTTP2
import ContainerdGRPC
import EdgeAgentGRPC
import Foundation
import Logging
import Crypto
#if canImport(Musl)
import Musl
#endif

struct NamespaceInterceptor: ClientInterceptor {
    func intercept<Input, Output>(
        request: StreamingClientRequest<Input>,
        context: ClientContext,
        next: (StreamingClientRequest<Input>, ClientContext) async throws -> StreamingClientResponse<Output>
    ) async throws -> StreamingClientResponse<Output> where Input : Sendable, Output : Sendable {
        var request = request
        request.metadata.addString("default", forKey: "containerd-namespace")
        return try await next(request, context)
    }
}

public struct Containerd: Sendable {
    let client: GRPCClient<HTTP2ClientTransport.Posix>
    let logger = Logger(label: "Containerd")

    public static func withClient<R: Sendable>(
        _ run: @escaping (Containerd) async throws -> R
    ) async throws -> R {
        let transport = try HTTP2ClientTransport.Posix(
            target: .unixDomainSocket(path: "/run/containerd/containerd.sock"),
            transportSecurity: .plaintext
        )
        return try await withGRPCClient(transport: transport, interceptors: [NamespaceInterceptor()]) { client in
            let client = Containerd(client: client)
            return try await run(client)
        }
    }

    public struct LayerWriter: Sendable {
        let ref: String
        let writer: RPCWriter<Containerd_Services_Content_V1_WriteContentRequest>
        fileprivate var offset: Int64 = 0

        init(ref: String, writer: RPCWriter<Containerd_Services_Content_V1_WriteContentRequest>) {
            self.ref = ref
            self.writer = writer
        }

        public mutating func write(data: Data) async throws {
            try await writer.write(.with {
                $0.data = data
                $0.offset = offset
                $0.ref = ref
                $0.action = .write
            })
            offset += Int64(data.count)
        }
    }

    public func uploadJSON(_ config: some Encodable) async throws -> (digest: String, size: Int64) {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        encoder.dateEncodingStrategy = .iso8601
        let encoded = try encoder.encode(config)
        let digest = SHA256.hash(data: encoded)
            .map { String(format: "%02x", $0) }.joined()
        let size = Int64(encoded.count)
        do {
            try await writeLayer(ref: digest) { writer in
                try await writer.write(data: encoded)
            }
        } catch let error as RPCError where error.code == .alreadyExists {
            // Ignore
        }
        return (digest, size)
    }

    public func writeLayer(
        ref: String,
        withWriter: @Sendable @escaping (inout LayerWriter) async throws -> Void
    ) async throws {
        let content = Containerd_Services_Content_V1_Content.Client(wrapping: client)
        try await content.write { writer in
            // TODO: Associate labels with the layer (attach to app)
            var layerWriter = LayerWriter(ref: ref, writer: writer)
            try await withWriter(&layerWriter)

            try await writer.write(.with {
                $0.ref = ref
                $0.offset = layerWriter.offset
                $0.action = .commit
            })
        } onResponse: { response in
            for try await _ in response.messages { }
        }
    }

    public func listContent(
        withContent: @Sendable @escaping ([Containerd_Services_Content_V1_Info]) async throws -> Void
    ) async throws {
        let content = Containerd_Services_Content_V1_Content.Client(wrapping: client)
        return try await content.list(
            request: .init(message: .with { req in
                // No filters
            })
        ) { response in
            for try await items in response.messages {
                try await withContent(items.info)
            }
        }
    }

    public func deleteImage(named name: String) async throws {
        let images = Containerd_Services_Images_V1_Images.Client(wrapping: client)
        try await images.delete(.with {
            $0.name = name
        })
    }

    public func createImage(named name: String, manifestHash: String, manifestSize: Int64) async throws {
        let images = Containerd_Services_Images_V1_Images.Client(wrapping: client)
        try await images.create(.with {
            $0.image = .with {
                $0.name = name
                $0.target = .with {
                    $0.mediaType = "application/vnd.oci.image.manifest.v1+json"
                    $0.digest = "sha256:\(manifestHash)"
                    $0.size = manifestSize
                }
            }
        }) { res in
            if case .failure(let error) = res.accepted {
                logger.error("Failed to create image", metadata: [
                    "image-name": .stringConvertible(name),
                    "manifest-digest": .stringConvertible("sha256:\(manifestHash)"),
                    "manifest-size": .stringConvertible(manifestSize),
                    "error": .stringConvertible(error.description)
                ])
                throw error
            }
        }
    }

    public func updateImage(named name: String, manifestHash: String, manifestSize: Int64) async throws {
        let images = Containerd_Services_Images_V1_Images.Client(wrapping: client)
        try await images.update(.with {
            $0.image = .with {
                $0.name = name
                $0.target = .with {
                    $0.mediaType = "application/vnd.oci.image.manifest.v1+json"
                    $0.digest = "sha256:\(manifestHash)"
                    $0.size = manifestSize
                }
            }
        }) { res in
            if case .failure(let error) = res.accepted {
                logger.error("Failed to update image", metadata: [
                    "image-name": .stringConvertible(name),
                    "manifest-digest": .stringConvertible("sha256:\(manifestHash)"),
                    "manifest-size": .stringConvertible(manifestSize),
                    "error": .stringConvertible(error.description)
                ])
                throw error
            }
        }
    }

    public func deleteContainer(named name: String) async throws {
        let containers = Containerd_Services_Containers_V1_Containers.Client(wrapping: client)
        try await containers.delete(.with {
            $0.id = name
        })
    }

    public func createSnapshot(
        imageName: String,
        layers: [Edge_Agent_Services_V1_RunContainerLayerHeader]
    ) async throws -> (snapshotKey: String?, mounts: [Containerd_Types_Mount]) {
        let snapshots = Containerd_Services_Snapshots_V1_Snapshots.Client(wrapping: client)
        let diffs = Containerd_Services_Diff_V1_Diff.Client(wrapping: client)
        var layers = layers

        if layers.isEmpty {
            return (snapshotKey: nil, mounts: [])
        }

        var layer = layers.removeFirst()
        var layerIndex = 0

        logger.info("Preparing snapshot for layer", metadata: [
            "layer-diff-id": .stringConvertible(layer.diffID),
        ])
        let snapshot = try await snapshots.prepare(.with {
            $0.key = "tmp-snapshot-\(layerIndex)-\(imageName)"
            $0.snapshotter = "overlayfs"
        })
        logger.info("Prepared snapshot", metadata: [
            "layer-diff-id": .stringConvertible(layer.diffID),
            "layer-digest": .stringConvertible(layer.digest),
            "layer-size": .stringConvertible(layer.size),
            "layer-gzip": .stringConvertible(layer.gzip),
            "layer-media-type": .stringConvertible(layer.gzip ? "application/vnd.oci.image.layer.v1.tar+gzip" : "application/vnd.oci.image.layer.v1.tar"),
            "snapshot-mounts": .stringConvertible(snapshot.mounts.map { $0.source }.joined(separator: ", "))
        ])
        var apply = try await diffs.apply(.with {
            $0.diff = .with {
                $0.digest = layer.digest
                $0.size = layer.size
                $0.mediaType = layer.gzip ? "application/vnd.oci.image.layer.v1.tar+gzip" : "application/vnd.oci.image.layer.v1.tar"
            }
            $0.mounts = snapshot.mounts
        })
        logger.info("Applied diff", metadata: [
            "layer-diff-id": .stringConvertible(layer.diffID),
            "layer-digest": .stringConvertible(layer.digest),
            "layer-size": .stringConvertible(layer.size),
            "layer-gzip": .stringConvertible(layer.gzip),
            "layer-media-type": .stringConvertible(layer.gzip ? "application/vnd.oci.image.layer.v1.tar+gzip" : "application/vnd.oci.image.layer.v1.tar"),
        ])
        try await snapshots.commit(.with {
            $0.key = "tmp-snapshot-\(layerIndex)-\(imageName)"
            $0.name = apply.applied.digest
            $0.snapshotter = "overlayfs"
        })
        logger.info("Committed snapshot", metadata: [
            "layer-diff-id": .stringConvertible(layer.diffID),
            "layer-digest": .stringConvertible(layer.digest),
            "layer-size": .stringConvertible(layer.size),
            "layer-gzip": .stringConvertible(layer.gzip),
            "layer-media-type": .stringConvertible(layer.gzip ? "application/vnd.oci.image.layer.v1.tar+gzip" : "application/vnd.oci.image.layer.v1.tar"),
        ])
        var mounts = snapshot.mounts

        while !layers.isEmpty {
            layerIndex += 1
            let nextLayer = layers.removeFirst()

            logger.info("Preparing snapshot for layer", metadata: [
                "layer-diff-id": .stringConvertible(nextLayer.diffID),
            ])
            let snapshot = try await snapshots.prepare(.with {
                $0.key = "tmp-snapshot-\(layerIndex)-\(imageName)"
                $0.parent = apply.applied.digest
                $0.snapshotter = "overlayfs"
            })

            logger.info("Prepared snapshot", metadata: [
                "layer-diff-id": .stringConvertible(nextLayer.diffID),
                "layer-digest": .stringConvertible(nextLayer.digest),
                "layer-size": .stringConvertible(nextLayer.size),
                "layer-gzip": .stringConvertible(nextLayer.gzip),
                "layer-media-type": .stringConvertible(nextLayer.gzip ? "application/vnd.oci.image.layer.v1.tar+gzip" : "application/vnd.oci.image.layer.v1.tar"),
            ])

            apply = try await diffs.apply(.with {
                $0.diff = .with {
                    $0.digest = nextLayer.digest
                    $0.size = nextLayer.size
                    $0.mediaType = nextLayer.gzip ? "application/vnd.oci.image.layer.v1.tar+gzip" : "application/vnd.oci.image.layer.v1.tar"
                }
                $0.mounts = snapshot.mounts
            })
            logger.info("Applied diff", metadata: [
                "layer-diff-id": .stringConvertible(nextLayer.diffID),
                "layer-digest": .stringConvertible(nextLayer.digest),
                "layer-size": .stringConvertible(nextLayer.size),
                "layer-gzip": .stringConvertible(nextLayer.gzip),
                "layer-media-type": .stringConvertible(nextLayer.gzip ? "application/vnd.oci.image.layer.v1.tar+gzip" : "application/vnd.oci.image.layer.v1.tar"),
            ])

            try await snapshots.commit(.with {
                $0.key = "tmp-snapshot-\(layerIndex)-\(imageName)"
                $0.name = apply.applied.digest
                $0.snapshotter = "overlayfs"
            })
            logger.info("Committed snapshot", metadata: [
                "layer-diff-id": .stringConvertible(nextLayer.diffID),
                "layer-digest": .stringConvertible(nextLayer.digest),
                "layer-size": .stringConvertible(nextLayer.size),
                "layer-gzip": .stringConvertible(nextLayer.gzip),
                "layer-media-type": .stringConvertible(nextLayer.gzip ? "application/vnd.oci.image.layer.v1.tar+gzip" : "application/vnd.oci.image.layer.v1.tar"),
            ])

            mounts = snapshot.mounts
            layer = nextLayer
        }

        return (snapshotKey: apply.applied.digest, mounts: mounts)
    }

    public func createContainer(
        imageName: String,
        appName: String,
        snapshotKey: String,
        ociSpec spec: Data
    ) async throws {
        let containers = Containerd_Services_Containers_V1_Containers.Client(wrapping: client)
        try await containers.create(.with {
            $0.container = .with {
                $0.id = appName
                $0.runtime = .with {
                    $0.name = "io.containerd.runc.v2"
                }
                $0.spec = .with {
                    $0.typeURL = "types.containerd.io/opencontainers/runtime-spec/1/Spec"
                    $0.value = spec
                }
                $0.snapshotter = "overlayfs"
                $0.snapshotKey = snapshotKey
                $0.image = imageName
            }
        }) { res in
            if case .failure(let error) = res.accepted {
                logger.error("Failed to create container", metadata: [
                    "app-name": .stringConvertible(appName),
                    "image-name": .stringConvertible(imageName),
                    "error": .stringConvertible(error.description)
                ])
                throw error
            }
        }
    }

    public func updateContainer(imageName: String, appName: String, snapshotKey: String, ociSpec: Data) async throws {
        do {
            let containers = Containerd_Services_Containers_V1_Containers.Client(wrapping: client)
            try await containers.update(.with {
                $0.container = .with {
                    $0.id = appName
                    $0.runtime = .with {
                        $0.name = "io.containerd.runc.v2"
                    }
                    $0.spec = .with {
                        $0.typeURL = "types.containerd.io/opencontainers/runtime-spec/1/Spec"
                        $0.value = ociSpec
                    }
                    $0.snapshotter = "overlayfs"
                    $0.snapshotKey = snapshotKey
                    $0.image = imageName
                }
            })
        } catch let error as RPCError {
            logger.error("Failed to update container", metadata: [
                "app-name": .stringConvertible(appName),
                "image-name": .stringConvertible(imageName),
                "snapshot-key": .stringConvertible(snapshotKey),
                "error": .stringConvertible(error.description)
            ])
            throw error
        }
    }

    public func createTask(
        containerID: String,
        appName: String,
        snapshotName: String,
        mounts: [Containerd_Types_Mount]
    ) async throws {
        let tasks = Containerd_Services_Tasks_V1_Tasks.Client(wrapping: client)
        do {
            let result = try await tasks.create(.with {
                $0.containerID = containerID
                $0.runtimePath = "io.containerd.runc.v2"
                $0.rootfs = mounts
            })
        } catch let error as RPCError {
            logger.error("Failed to create task", metadata: [
                "container-id": .stringConvertible(containerID),
                "app-name": .stringConvertible(appName),
                "error": .stringConvertible(error.description)
            ])
            throw error
        }
    }

    public func deleteTask(containerID: String) async throws {
        let tasks = Containerd_Services_Tasks_V1_Tasks.Client(wrapping: client)
        try await tasks.delete(.with {
            $0.containerID = containerID
        })
    }

    public func runTask(containerID: String) async throws -> String {
        let execID = UUID().uuidString
        let tasks = Containerd_Services_Tasks_V1_Tasks.Client(wrapping: client)
        try await tasks.start(.with {
            $0.containerID = containerID
            $0.execID = execID
        }) { res in
            if case .failure(let error) = res.accepted {
                logger.error("Failed to run container", metadata: [
                    "container-id": .stringConvertible(containerID),
                    "exec-id": .stringConvertible(execID),
                    "error": .stringConvertible(error.description)
                ])
                throw error
            }
        }
        return execID
    }
}