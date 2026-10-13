// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "WendyCore",
    platforms: [.macOS(.v15)],
    products: [.library(name: "WendyCore", targets: ["WendyCore"])],
    targets: [
        .target(
            name: "CWendyCoreBridge",
            publicHeadersPath: "include",
            linkerSettings: [.linkedLibrary("dl", .when(platforms: [.linux]))]
        ),
        .target(
            name: "WendyCore",
            dependencies: ["CWendyCoreBridge"],
            resources: [.copy("native")],
            swiftSettings: [.enableUpcomingFeature("InternalImportsByDefault")]
        ),
        .testTarget(name: "WendyCoreTests", dependencies: ["WendyCore"]),
    ]
)
