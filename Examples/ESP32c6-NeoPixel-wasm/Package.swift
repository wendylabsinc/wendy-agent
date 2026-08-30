// swift-tools-version: 6.3
import PackageDescription

let package = Package(
    name: "ESP32C6NeoPixelWasm",
    dependencies: [
        .package(
            url: "https://github.com/wendylabsinc/wendy-lite.git",
            branch: "main"
        ),
    ],
    targets: [
        .executableTarget(
            name: "ESP32C6NeoPixelWasm",
            dependencies: [
                .product(name: "WendyLite", package: "wendy-lite"),
            ],
            swiftSettings: [
                .enableExperimentalFeature("Embedded"),
                .unsafeFlags(["-wmo"]),
            ],
            linkerSettings: [
                .unsafeFlags([
                    "-Xlinker", "--allow-undefined",
                    "-Xlinker", "--initial-memory=65536",
                    "-Xlinker", "--table-base=1",
                    "-Xlinker", "--strip-all",
                    "-Xlinker", "--export=malloc",
                    "-Xlinker", "--export=free",
                    "-Xlinker", "--export=wendy_handle_callback",
                    "-Xlinker", "-z", "-Xlinker", "stack-size=8192",
                ]),
            ]
        ),
    ]
)
