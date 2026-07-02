// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "MLXProbe",
    platforms: [
        .macOS(.v15)
    ],
    dependencies: [
        // kb/linux-image-path adds the platform-neutral (pure-MLX) image
        // preprocessing that makes VLM vision input work on Linux.
        .package(url: "https://github.com/wendylabsinc/mlx-swift-lm.git", branch: "kb/linux-image-path"),
        .package(url: "https://github.com/wendylabsinc/gstreamer-swift.git", branch: "main"),
        .package(url: "https://github.com/apple/swift-argument-parser.git", from: "1.7.1"),
    ],
    targets: [
        .executableTarget(
            name: "MLXProbe",
            dependencies: [
                .product(name: "MLXVLM", package: "mlx-swift-lm"),
                .product(name: "MLXLMCommon", package: "mlx-swift-lm"),
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
            ]
        ),
        .executableTarget(
            name: "MLXVisionProbe",
            dependencies: [
                .product(name: "MLXVLM", package: "mlx-swift-lm"),
                .product(name: "MLXLMCommon", package: "mlx-swift-lm"),
                .product(name: "GStreamer", package: "gstreamer-swift"),
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
            ]
        ),
    ]
)
