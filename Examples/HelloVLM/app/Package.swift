// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "HelloVLM",
    platforms: [
        .macOS(.v15)
    ],
    dependencies: [
        .package(url: "https://github.com/swhitty/FlyingFox.git", from: "0.20.0"),
        .package(url: "https://github.com/apple/swift-argument-parser.git", from: "1.7.1"),
        .package(url: "https://github.com/apple/swift-container-plugin", from: "1.3.0"),
    ],
    targets: [
        .systemLibrary(
            name: "CLinuxVideo"
        ),
        .executableTarget(
            name: "HelloVLM",
            dependencies: [
                .product(name: "FlyingFox", package: "FlyingFox"),
                .product(name: "FlyingSocks", package: "FlyingFox"),
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
                .target(name: "CLinuxVideo", condition: .when(platforms: [.linux])),
            ],
            resources: [
                .copy("Resources")
            ]
        ),
    ]
)
