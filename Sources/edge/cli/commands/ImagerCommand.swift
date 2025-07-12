import ArgumentParser
import Foundation
import Imager

#if os(macOS)
    import Darwin
#elseif os(Linux)
    // No explicit libc import needed when using musl
#endif

// Helper function to safely flush output without referring to stdout directly
// This avoids concurrency issues with global variables
@inline(__always) private func flushOutput() {
    // Simply print an empty string with a newline to force flush
    // This is a simple workaround that doesn't require accessing global C variables
    print("", terminator: "")
}

struct ImagerCommand: AsyncParsableCommand {

    static let configuration = CommandConfiguration(
        commandName: "imager",
        abstract: "Image EdgeOS projects.",
        subcommands: [
            ListCommand.self, ListDevicesCommand.self, WriteCommand.self, WriteDeviceCommand.self,
        ]
    )

    struct ListCommand: AsyncParsableCommand {

        static let configuration = CommandConfiguration(
            commandName: "list",
            abstract: "List available external drives."
        )

        @Flag(name: .long, help: "List all drives, not just external drives")
        var all: Bool = false

        @Flag(name: [.customShort("j"), .long], help: "Output in JSON format")
        var json: Bool = false

        func run() async throws {
            let diskLister = DiskListerFactory.createDiskLister()
            let drives = try await diskLister.list(all: all)

            if json {
                let jsonString = try JSONEncoder().encode(drives)
                print(String(data: jsonString, encoding: .utf8)!)
            } else if drives.isEmpty {
                print("No external drives found.")
            } else {
                print("\nAvailable external drives:")
                print("---------------------------")

                for (index, drive) in drives.enumerated() {
                    print("[\(index + 1)] \(drive.name) (\(drive.id))")
                    print("    Capacity: \(drive.capacityHumanReadableText)")
                    print("    Available: \(drive.availableHumanReadableText)")
                    print("    Type: \(drive.isExternal ? "External" : "Internal")")
                    print("")
                }
            }
        }
    }

    struct ListDevicesCommand: AsyncParsableCommand {

        static let configuration = CommandConfiguration(
            commandName: "list-devices",
            abstract: "List available device images."
        )

        @Flag(name: [.customShort("j"), .long], help: "Output in JSON format")
        var json: Bool = false

        func run() async throws {
            if !json {
                print("📱 Fetching available device images...")
            }

            let manifestManager = ManifestManagerFactory.createManifestManager()
            let deviceList = try await manifestManager.getAvailableDevices()

            if json {
                let jsonString = try JSONEncoder().encode(deviceList)
                print(String(data: jsonString, encoding: .utf8)!)
            } else if deviceList.isEmpty {
                print("No devices found in the manifest.")
            } else {
                print("\nAvailable devices:")
                print("------------------")

                for (index, deviceInfo) in deviceList.enumerated() {
                    print("[\(index + 1)] \(deviceInfo.name)")
                    if !deviceInfo.latestVersion.isEmpty {
                        print("    Latest version: \(deviceInfo.latestVersion)")
                    } else {
                        print("    No version available")
                    }
                    print("")
                }

                print("To write a device image: edge imager write-device <device-name> <drive-id>")
                print("Example: edge imager write-device raspberry-pi-5 disk2")
            }
        }
    }

    struct WriteCommand: AsyncParsableCommand {

        static let configuration = CommandConfiguration(
            commandName: "write",
            abstract: "Write an image to a drive."
        )

        @Argument(help: "Path to the image file to write")
        var imagePath: String

        @Argument(help: "Target drive to write to")
        var driveId: String

        func run() async throws {
            // Use DiskLister to find the drive
            let diskLister = DiskListerFactory.createDiskLister()
            let drive = try await diskLister.findDrive(byId: driveId)

            // Use DiskWriter to write the image
            let diskWriter = DiskWriterFactory.createDiskWriter()

            // Create a progress bar
            print("Starting to write image to \(drive.name) (\(drive.id))")
            print("Press Ctrl+C to cancel")

            // Track the last update time to avoid too frequent updates
            var lastUpdateTime = Date()

            try await diskWriter.write(imagePath: imagePath, drive: drive) { progress in
                // Update progress at most once per second to avoid flooding the terminal
                let now = Date()
                if now.timeIntervalSince(lastUpdateTime) >= 1.0 {
                    lastUpdateTime = now

                    if let percent = progress.percentComplete {
                        let line: String
                        if let totalText = progress.totalBytesText {
                            line = "\u{1B}[1G\u{1B}[2K   \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)/\(totalText)"
                        } else {
                            line = "\u{1B}[1G\u{1B}[2K   \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)"
                        }

                        print(line, terminator: "")
                        fflush(stdout)
                    } else {
                        // Fallback when percentage isn’t available
                        print("\u{1B}[1G\u{1B}[2KWritten: \(progress.bytesWrittenText)", terminator: "")
                        fflush(stdout)
                    }
                }
            }

            // Clear the line and print completion message
            print("\r\u{1B}[K", terminator: "")
            print("✅ Image successfully written to \(drive.name)")
        }
    }

    struct WriteDeviceCommand: AsyncParsableCommand {

        static let configuration = CommandConfiguration(
            commandName: "write-device",
            abstract: "Download and write the latest image for a specific device."
        )

        @Argument(help: "Device name (e.g., raspberry-pi-5)")
        var deviceName: String

        @Argument(help: "Target drive to write to")
        var driveId: String

        @Flag(name: .long, help: "Skip confirmation before writing")
        var force: Bool = false

        @Flag(name: .long, help: "Force redownload and write the image")
        var redownload: Bool = false

        func run() async throws {
            // Use DiskLister to find the drive
            let diskLister = DiskListerFactory.createDiskLister()
            let drive = try await diskLister.findDrive(byId: driveId)

            print("🔍 Finding latest image for \(deviceName)...")

            // Get the latest image information for the device
            let manifestManager = ManifestManagerFactory.createManifestManager()
            let (imageUrl, imageSize) = try await manifestManager.getLatestImageInfo(
                for: deviceName
            )

            print("📥 Found image: \(imageUrl.lastPathComponent)")
            print(
                "   Size: \(ByteCountFormatter.string(fromByteCount: Int64(imageSize), countStyle: .file))"
            )

            // Confirm with the user before proceeding
            if !force {
                print("\n⚠️  WARNING: All data on \(drive.name) (\(drive.id)) will be erased.")
                print("   Type 'yes' to continue or any other key to abort:")

                let response = readLine()?.lowercased()
                guard response == "yes" else {
                    print("Operation aborted.")
                    return
                }
            }

            // Download the image
            print("\n📥 Downloading image...")
            let imageDownloader = ImageDownloaderFactory.createImageDownloader()
            var lastUpdateTime = Date()

            let localImagePath = try await imageDownloader.downloadImage(
                from: imageUrl,
                deviceName: deviceName,
                expectedSize: imageSize,
                redownload: redownload
            ) { progress in
                let now = Date()
                if now.timeIntervalSince(lastUpdateTime) >= 1.0 {
                    lastUpdateTime = now

                    if let percent = progress.percentComplete {
                        let line: String
                        if let totalText = progress.totalBytesText {
                            line = "\u{1B}[1G\u{1B}[2K   \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)/\(totalText)"
                        } else {
                            line = "\u{1B}[1G\u{1B}[2K   \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)"
                        }

                        print(line, terminator: "")
                        fflush(stdout)
                    } else {
                        // Fallback when percentage isn’t available
                        print("\u{1B}[1G\u{1B}[2KWritten: \(progress.bytesWrittenText)", terminator: "")
                        fflush(stdout)
                    }
                }
            }

            // Clear the line and print completion message
            print("\r\u{1B}[K", terminator: "")
            print("✅ Image downloaded to: \(localImagePath)")
            print("\n💾 Writing image to \(drive.name) (\(drive.id))...")
            print("   Press Ctrl+C to cancel")

            // Use DiskWriter to write the image
            let diskWriter = DiskWriterFactory.createDiskWriter()
            lastUpdateTime = Date()

            try await diskWriter.write(imagePath: localImagePath, drive: drive) { progress in
                // Update progress at most once per second
                let now = Date()
                if now.timeIntervalSince(lastUpdateTime) >= 1.0 {
                    lastUpdateTime = now

                    if let percent = progress.percentComplete {
                        let line: String
                        if let totalText = progress.totalBytesText {
                            line = "\u{1B}[1G\u{1B}[2K   \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)/\(totalText)"
                        } else {
                            line = "\u{1B}[1G\u{1B}[2K   \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)"
                        }

                        print(line, terminator: "")
                        fflush(stdout)
                    } else {
                        // Fallback when percentage isn't available
                        print("\u{1B}[1G\u{1B}[2KWritten: \(progress.bytesWrittenText)", terminator: "")
                        fflush(stdout)
                    }
                }
            }

            // Clear the line and print completion message
            print("\r\u{1B}[K", terminator: "")
            print("✅ Image successfully written to \(drive.name)")
            print("\n🎉 Device \(deviceName) successfully imaged!")
        }
    }
}
