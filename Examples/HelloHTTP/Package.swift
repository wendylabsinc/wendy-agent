// swift-tools-version: 6.0
// The swift-tools-version declares the minimum version of Swift required to build this package.

import PackageDescription

let package = Package(
    name: "HelloHTTP",
    platforms: [
        .macOS(.v15)
    ],
    dependencies: [
        .package(url: "https://github.com/hummingbird-project/hummingbird.git", from: "2.10.0"),
        .package(url: "https://github.com/swift-otel/swift-otel.git", from: "1.0.0"),
    ],
    targets: [
        // Targets are the basic building blocks of a package, defining a module or a test suite.
        // Targets can depend on other targets in this package and products from dependencies.
        .executableTarget(
            name: "HelloHTTP",
            dependencies: [
                .product(name: "Hummingbird", package: "hummingbird"),
                .product(name: "OTel", package: "swift-otel"),
            ]
        )
    ]
)
