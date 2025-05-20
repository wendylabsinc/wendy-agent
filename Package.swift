// swift-tools-version: 6.0.3
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
        .package(url: "https://github.com/swift-server/async-http-client.git", from: "1.25.2"),
        .package(url: "https://github.com/apple/swift-argument-parser.git", from: "1.5.0"),
        .package(url: "https://github.com/apple/swift-log.git", from: "1.6.3"),
        .package(url: "https://github.com/grpc/grpc-swift.git", from: "2.0.0"),
        .package(url: "https://github.com/grpc/grpc-swift-protobuf.git", from: "1.0.0"),
        .package(url: "https://github.com/grpc/grpc-swift-nio-transport.git", from: "1.0.0"),
        .package(url: "https://github.com/swift-server/swift-service-lifecycle.git", from: "2.7.0"),
        .package(url: "https://github.com/grpc/grpc-swift-extras.git", from: "1.0.0"),
        .package(url: "https://github.com/apple/swift-nio.git", from: "2.81.0"),
        .package(url: "https://github.com/apple/swift-crypto.git", from: "3.12.2"),
        .package(url: "https://github.com/swiftlang/swift-subprocess.git", branch: "main"),
        .package(url: "https://github.com/apple/swift-http-types.git", from: "1.4.0"),
        .package(url: "https://github.com/apple/swift-async-dns-resolver.git", from: "0.4.0"),
        .package(url: "https://github.com/edgeengineer/dbus.git", from:"0.1.0"),  
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
                .product(name: "AsyncDNSResolver", package: "swift-async-dns-resolver"),
                .target(name: "EdgeAgentGRPC"),
                .target(name: "EdgeCLI"),
                .target(name: "EdgeShared"),
                .target(name: "Imager"),
                .target(name: "ContainerRegistry"),
                .target(name: "DownloadSupport"),
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
                .product(name: "Subprocess", package: "swift-subprocess"),
                .product(name: "Logging", package: "swift-log"),
            ]
        ),

        /// Tools to build OCI-compliant container images.
        .target(
            name: "ContainerBuilder",
            dependencies: [
                .target(name: "Shell"),
                .product(name: "Crypto", package: "swift-crypto"),
                .target(name: "ContainerRegistry"),
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
                .product(name: "DBusSwift", package: "dbus"),
                .target(name: "EdgeAgentGRPC"),
                .target(name: "Shell"),
                .target(name: "EdgeShared"),
                
            ]
        ),

        /// Shared components used by both edge and edge-agent.
        .target(
            name: "ContainerRegistry",
            dependencies: [
                .product(name: "Crypto", package: "swift-crypto"),
                .product(name: "HTTPTypes", package: "swift-http-types"),
                .product(name: "HTTPTypesFoundation", package: "swift-http-types"),
            ]
        ),
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
            name: "Imager",
            dependencies: [
                .product(name: "Subprocess", package: "swift-subprocess"),
                .product(name: "AsyncHTTPClient", package: "async-http-client"),
                .product(name: "_NIOFileSystem", package: "swift-nio"),
                .target(name: "DownloadSupport"),
            ]
        ),
        .target(
            name: "DownloadSupport",
            dependencies: [
                .product(name: "AsyncHTTPClient", package: "async-http-client"),
                .product(name: "_NIOFileSystem", package: "swift-nio"),
            ]
        ),
        .target(
            name: "Shell",
            dependencies: [
                .product(name: "Logging", package: "swift-log")
            ]
        ),

        /// Tests for EdgeCLI components
        .testTarget(
            name: "EdgeCLITests",
            dependencies: [
                .target(name: "edge")
            ]
        ),

    ]
)
