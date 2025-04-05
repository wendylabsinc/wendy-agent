// swift-tools-version: 6.1
import PackageDescription

let package = Package(
    name: "edge-agent",
    platforms: [
        .macOS(.v15)
    ],
    products: [
        .executable(name: "edge-agent", targets: ["edge-agent"])
    ],
    dependencies: [
        .package(
            url: "https://github.com/apache-edge/edge-agent-common.git",
            revision: "952035635c630d366dfbcff04af1934c7c051f23"
        ),
        .package(url: "https://github.com/apple/swift-argument-parser.git", from: "1.5.0"),
        .package(url: "https://github.com/apple/swift-log.git", from: "1.6.3"),
        .package(url: "https://github.com/grpc/grpc-swift-nio-transport.git", from: "1.0.0"),
        .package(url: "https://github.com/swift-server/swift-service-lifecycle.git", from: "2.7.0"),
        .package(url: "https://github.com/grpc/grpc-swift-extras.git", from: "1.0.0"),
        .package(url: "https://github.com/apple/swift-nio.git", from: "2.81.0"),
    ],
    targets: [
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
                .product(name: "EdgeAgentGRPC", package: "edge-agent-common"),
                .product(name: "Shell", package: "edge-agent-common"),
            ]
        )
    ]
)
