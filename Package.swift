// swift-tools-version: 6.1.2
import PackageDescription

#if compiler(>=6.2.1)
    let hasSpan = true
#else
    let hasSpan = false
#endif

let package = Package(
    name: "edge-agent",
    platforms: [
        .macOS(.v15)
    ],
    products: [
        .executable(name: "edge-agent", targets: ["edge-agent"]),
        .executable(name: "edge-agent-updater", targets: ["edge-agent-updater"]),
        .executable(name: "edge", targets: ["edge"]),
        .executable(name: "edge-helper", targets: ["edge-helper"]),
        .executable(name: "edge-network-daemon", targets: ["edge-network-daemon"]),
    ],
    dependencies: [
        .package(url: "https://github.com/swift-server/async-http-client.git", from: "1.25.2"),
        .package(url: "https://github.com/apple/swift-argument-parser.git", from: "1.5.0"),
        .package(url: "https://github.com/apple/swift-log.git", from: "1.6.3"),
        .package(url: "https://github.com/grpc/grpc-swift-2.git", from: "2.0.0"),
        .package(url: "https://github.com/grpc/grpc-swift-protobuf.git", from: "2.0.0"),
        .package(url: "https://github.com/grpc/grpc-swift-nio-transport.git", from: "2.0.0"),
        .package(url: "https://github.com/apple/swift-protobuf.git", from: "1.30.0"),
        .package(url: "https://github.com/swift-server/swift-service-lifecycle.git", from: "2.7.0"),
        .package(url: "https://github.com/apple/swift-nio.git", from: "2.81.0"),
        .package(url: "https://github.com/apple/swift-crypto.git", from: "3.12.2"),
        .package(
            url: "https://github.com/swiftlang/swift-subprocess.git",
            from: "0.1.0",
            traits: hasSpan ? [.trait(name: "SubprocessSpan")] : []
        ),
        .package(url: "https://github.com/apple/swift-http-types.git", from: "1.4.0"),
        .package(url: "https://github.com/apple/swift-async-dns-resolver.git", from: "0.4.0"),
        .package(url: "https://github.com/apple/swift-openapi-generator.git", from: "1.10.3"),
        .package(
            url: "https://github.com/swift-server/swift-openapi-async-http-client.git",
            from: "1.1.0"
        ),
        //.package(url: "https://github.com/edgeengineer/dbus.git", from: "0.2.2"),
        .package(path: "/Users/joannisorlandos/git/edge/dbus"),
        .package(url: "https://github.com/apple/swift-system.git", from: "1.4.2"),
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
                .product(name: "SystemPackage", package: "swift-system"),
                .product(
                    name: "OpenAPIAsyncHTTPClient",
                    package: "swift-openapi-async-http-client"
                ),
                .target(name: "EdgeAgentGRPC"),
                .target(name: "EdgeCLI"),
                .target(name: "EdgeShared"),
                .target(name: "Imager"),
                .target(name: "ContainerRegistry"),
                .target(name: "DownloadSupport"),
                .target(name: "AppConfig"),
                .target(name: "CliXPCProtocol"),
                .target(name: "DockerOpenAPI"),
            ],
            path: "Sources/Edge",
            resources: [
                .copy("Resources")
            ]
        ),

        .target(
            name: "DockerOpenAPI",
            dependencies: [
                .product(name: "OpenAPIAsyncHTTPClient", package: "swift-openapi-async-http-client")
            ],
            plugins: [.plugin(name: "OpenAPIGenerator", package: "swift-openapi-generator")]
        ),

        /// Contains everything EdgeCLI, except for the command line interface.
        .target(
            name: "EdgeCLI",
            dependencies: [
                .target(name: "ContainerBuilder"),
                .product(name: "Subprocess", package: "swift-subprocess"),
                .product(name: "Logging", package: "swift-log"),
            ]
        ),

        /// Tools to build OCI-compliant container images.
        .target(
            name: "ContainerBuilder",
            dependencies: [
                .product(name: "Subprocess", package: "swift-subprocess"),
                .product(name: "_NIOFileSystem", package: "swift-nio"),
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
                .product(name: "_NIOFileSystem", package: "swift-nio"),
                .product(name: "DBUS", package: "dbus"),
                .target(name: "EdgeAgentGRPC"),
                .target(name: "ContainerdGRPC"),
                .target(name: "ContainerRegistry"),
                .product(name: "Subprocess", package: "swift-subprocess"),
                .target(name: "EdgeShared"),
                .target(name: "AppConfig"),
            ],
            path: "Sources/EdgeAgent"
        ),

        /// The main executable provided by edge-agent.
        .executableTarget(
            name: "edge-agent-updater",
            dependencies: [
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
                .product(name: "Logging", package: "swift-log"),
                .product(name: "GRPCNIOTransportHTTP2", package: "grpc-swift-nio-transport"),
                .product(name: "ServiceLifecycle", package: "swift-service-lifecycle"),
                .product(name: "_NIOFileSystem", package: "swift-nio"),
                .target(name: "EdgeAgentGRPC"),
            ],
            path: "Sources/EdgeAgentUpdater"
        ),

        /// Shared components used by both edge and edge-agent.
        .target(
            name: "ContainerRegistry",
            dependencies: [
                .product(name: "Crypto", package: "swift-crypto"),
                .product(name: "HTTPTypes", package: "swift-http-types"),
                .product(name: "HTTPTypesFoundation", package: "swift-http-types"),
                .product(name: "AsyncHTTPClient", package: "async-http-client"),
            ]
        ),
        .target(
            name: "EdgeShared",
            dependencies: [
                .product(name: "Logging", package: "swift-log"),
                .product(name: "AsyncDNSResolver", package: "swift-async-dns-resolver"),
                .product(name: "Subprocess", package: "swift-subprocess"),
            ]
        ),
        .target(
            name: "EdgeAgentGRPC",
            dependencies: [
                .product(name: "GRPCCore", package: "grpc-swift-2"),
                .product(name: "GRPCProtobuf", package: "grpc-swift-protobuf"),
            ]
        ),
        .target(
            name: "ContainerdGRPC",
            dependencies: [
                .product(name: "GRPCCore", package: "grpc-swift-2"),
                .product(name: "GRPCProtobuf", package: "grpc-swift-protobuf"),
                .target(name: "ContainerdGRPCTypes"),
            ]
        ),
        .target(
            name: "ContainerdGRPCTypes",
            dependencies: [
                .product(name: "GRPCCore", package: "grpc-swift-2"),
                .product(name: "GRPCProtobuf", package: "grpc-swift-protobuf"),
            ],
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
                .product(name: "Subprocess", package: "swift-subprocess"),
            ]
        ),
        .target(
            name: "AppConfig",
            dependencies: [
                .product(name: "ArgumentParser", package: "swift-argument-parser")
            ]
        ),

        /// Tests for EdgeCLI components
        .testTarget(
            name: "EdgeCLITests",
            dependencies: [
                .target(name: "edge"),
                .target(name: "edge-agent"),
                .target(name: "EdgeAgentGRPC"),
                .target(name: "edge-helper", condition: .when(platforms: [.macOS])),
            ]
        ),

        /// The edge helper daemon for USB device monitoring
        .executableTarget(
            name: "edge-helper",
            dependencies: [
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
                .product(name: "Logging", package: "swift-log"),
                .product(name: "SystemPackage", package: "swift-system"),
                .target(name: "EdgeShared"),
                // Reuse existing device discovery components
                .target(name: "edge"),  // For device discovery protocols
            ],
            path: "Sources/EdgeHelper"
        ),

        /// XPC Protocol for communication between CLI and privileged daemon
        .target(
            name: "CliXPCProtocol",
            dependencies: []
        ),

        /// The privileged network daemon for macOS
        .executableTarget(
            name: "edge-network-daemon",
            dependencies: [
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
                .product(name: "Logging", package: "swift-log"),
                .target(name: "EdgeShared"),
                .target(name: "CliXPCProtocol"),
            ],
            path: "Sources/EdgeNetworkDaemon",
            exclude: [
                "edge-network-daemon.entitlements"
            ]
        ),

        .testTarget(
            name: "EdgeHelperMacOSTests",
            dependencies: [
                .target(name: "edge-helper"),
                .target(name: "EdgeShared"),
            ]
        ),

        .testTarget(
            name: "IntegrationTests",
            dependencies: [
                .target(name: "edge"),
                .target(name: "edge-agent"),
            ]
        ),

    ]
)
