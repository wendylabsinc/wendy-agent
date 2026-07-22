// swift-tools-version: 6.0
// The swift-tools-version declares the minimum version of Swift required to build this package.

import PackageDescription

let package = Package(
    name: "camserver",
    platforms: [
        .macOS(.v15)
    ],
    dependencies: [
        .package(url: "https://github.com/apple/swift-container-plugin", from: "1.0.0")
    ],
    targets: [
        // Targets are the basic building blocks of a package, defining a module or a test suite.
        // Targets can depend on other targets in this package and products from dependencies.
        .systemLibrary(
            name: "CLinuxVideo"
        ),
        .target(
            // Trimmed, JPEG-free port of HelloVideo's LinuxVideo module: opens a V4L2
            // device and hands back raw YUYV bytes / RGB-converted bytes. Linux-only
            // (imports CLinuxVideo, which needs <linux/videodev2.h>) — does not build
            // on macOS. HARDWARE-UNVERIFIED: never exercised against a real /dev/video*
            // device; only checked for type-correctness by reading against HelloVideo's
            // proven ioctl sequence.
            name: "CamCapture",
            dependencies: [
                .target(name: "CLinuxVideo")
            ]
        ),
        .target(
            // Wire protocol + raw POSIX socket plumbing (Glibc on Linux, Darwin on
            // macOS). No SwiftNIO, no Foundation networking types — plain
            // socket/bind/listen/accept/read/write. Platform-independent, so this is
            // the part of camserver that actually builds and type-checks on macOS.
            name: "RemoteCamWire"
        ),
        .executableTarget(
            name: "camserver",
            dependencies: [
                .target(name: "CamCapture"),
                .target(name: "CLinuxVideo"),
                .target(name: "RemoteCamWire"),
            ]
        ),
    ]
)
