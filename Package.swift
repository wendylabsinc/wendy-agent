// swift-tools-version: 6.1
import PackageDescription

let package = Package(
    name: "edge-agent",
    platforms: [
        .macOS(.v15)
    ],
    products: [
        .executable(name: "edge-agent", targets: ["edge-agent"]),
        .executable(name: "edge", targets: ["edge"]),
    ],
    dependencies: [
        .package(url: "https://github.com/apple/swift-argument-parser.git", from: "1.5.0"),
        .package(url: "https://github.com/apple/swift-log.git", from: "1.6.3"),
        .package(url: "https://github.com/grpc/grpc-swift.git", from: "2.0.0"),
        .package(url: "https://github.com/grpc/grpc-swift-protobuf.git", from: "1.0.0"),
        .package(url: "https://github.com/grpc/grpc-swift-nio-transport.git", from: "1.0.0"),
        .package(url: "https://github.com/swift-server/swift-service-lifecycle.git", from: "2.7.0"),
        .package(url: "https://github.com/grpc/grpc-swift-extras.git", from: "1.0.0"),
        .package(url: "https://github.com/apple/swift-nio.git", from: "2.81.0"),
        .package(url: "https://github.com/apple/swift-crypto.git", from: "3.12.2"),
    ],
    targets: [
        /// The main executable provided by edge-cli.
        .executableTarget(
            name: "edge",
            dependencies: [
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
                .product(name: "Logging", package: "swift-log"),
                .product(name: "_NIOFileSystem", package: "swift-nio"),
                .product(name: "GRPCNIOTransportHTTP2", package: "grpc-swift-nio-transport"),
                .target(name: "EdgeAgentGRPC"),
                .target(name: "EdgeCLI"),
                .target(name: "EdgeShared"),
            ],
            resources: [
                .copy("Resources")
            ]
        ),

        /// Contains everything EdgeCLI, except for the command line interface.
        .target(
            name: "EdgeCLI",
            dependencies: [
                .target(name: "ContainerBuilder"),
                .target(name: "Shell"),
                .product(name: "Logging", package: "swift-log"),
            ]
        ),

        /// Tools to build OCI-compliant container images.
        .target(
            name: "ContainerBuilder",
            dependencies: [
                .target(name: "Shell"),
                .product(name: "Crypto", package: "swift-crypto"),
            ]
        ),

        /// The main executable provided by edge-agent.
        .executableTarget(
            name: "edge-agent",
            dependencies: [
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
                .product(name: "Logging", package: "swift-log"),
                .product(name: "GRPCNIOTransportHTTP2", package: "grpc-swift-nio-transport"),
                .product(name: "ServiceLifecycle", package: "swift-service-lifecycle"),
                .product(name: "GRPCServiceLifecycle", package: "grpc-swift-extras"),
                .product(name: "GRPCHealthService", package: "grpc-swift-extras"),
                .product(name: "_NIOFileSystem", package: "swift-nio"),
                .target(name: "EdgeAgentGRPC"),
                .target(name: "Shell"),
                .target(name: "EdgeShared"),
            ]
        ),

        /// Shared components used by both edge and edge-agent.
        .target(
            name: "EdgeShared",
            dependencies: []
        ),

        .target(
            name: "EdgeAgentGRPC",
            dependencies: [
                .product(name: "GRPCCore", package: "grpc-swift"),
                .product(name: "GRPCProtobuf", package: "grpc-swift-protobuf"),
            ],
            exclude: [
                "Proto/edge_agent.protoset"
            ]
        ),

        .target(
            name: "Shell",
            dependencies: [
                .product(name: "Logging", package: "swift-log")
            ]
        ),

        .testTarget(
            name: "edgeTests",
            dependencies: [
                .target(name: "edge")
            ]
        ),
    ]
)
