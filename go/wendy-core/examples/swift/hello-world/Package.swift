// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "HelloWorld",
    platforms: [.macOS(.v15)],
    dependencies: [.package(path: "../../../distributions/swift")],
    targets: [
        .executableTarget(
            name: "HelloWorld",
            dependencies: [.product(name: "WendyCore", package: "swift")]
        ),
    ]
)
