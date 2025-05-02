import ArgumentParser
import Foundation
import Imager

#if os(macOS)
    import Darwin
#elseif os(Linux)
    import Glibc
#endif

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

        func run() async throws {
            let diskLister = DiskListerFactory.createDiskLister()
            let drives = try await diskLister.list(all: all)

            if drives.isEmpty {
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

        func run() async throws {
            print("📱 Fetching available device images...")

            let manifestManager = ManifestManagerFactory.createManifestManager()
            let deviceList = try await manifestManager.getAvailableDevices()

            if deviceList.isEmpty {
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

                    // Clear the current line
                    print("\r\u{1B}[K", terminator: "")

                    // Print progress information
                    if let percent = progress.percentComplete {
                        // Use the new ASCII progress bar
                        let progressBar = progress.asciiProgress(
                            totalBlocks: 30,
                            appendPercentageText: false
                        )

                        // Print progress with percentage and written/total bytes
                        if let totalText = progress.totalBytesText {
                            print(
                                "\r\(progressBar) \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)/\(totalText)",
                                terminator: ""
                            )
                        } else {
                            print(
                                "\r\(progressBar) \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)",
                                terminator: ""
                            )
                        }
                    } else {
                        // If we don't know the percentage, just show bytes written
                        print("\rWritten: \(progress.bytesWrittenText)", terminator: "")
                    }

                    // Flush stdout to ensure progress is displayed
                    fflush(stdout)
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
                expectedSize: imageSize
            ) { progress in
                // Update progress at most once per second
                let now = Date()
                if now.timeIntervalSince(lastUpdateTime) >= 1.0 {
                    lastUpdateTime = now

                    // Clear the current line
                    print("\r\u{1B}[K", terminator: "")

                    if let percent = progress.percentComplete {
                        // Use ASCII progress bar
                        let progressBar = progress.asciiProgress(
                            totalBlocks: 30,
                            appendPercentageText: false
                        )

                        // Print progress with percentage and downloaded/total bytes
                        if let totalText = progress.totalBytesText {
                            print(
                                "\r\(progressBar) \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)/\(totalText)",
                                terminator: ""
                            )
                        } else {
                            print(
                                "\r\(progressBar) \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)",
                                terminator: ""
                            )
                        }
                    } else {
                        // If we don't know the percentage, just show bytes downloaded
                        print("\rDownloaded: \(progress.bytesWrittenText)", terminator: "")
                    }

                    // Flush stdout to ensure progress is displayed
                    fflush(stdout)
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

                    // Clear the current line
                    print("\r\u{1B}[K", terminator: "")

                    if let percent = progress.percentComplete {
                        // Use ASCII progress bar
                        let progressBar = progress.asciiProgress(
                            totalBlocks: 30,
                            appendPercentageText: false
                        )

                        // Print progress with percentage and written/total bytes
                        if let totalText = progress.totalBytesText {
                            print(
                                "\r\(progressBar) \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)/\(totalText)",
                                terminator: ""
                            )
                        } else {
                            print(
                                "\r\(progressBar) \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)",
                                terminator: ""
                            )
                        }
                    } else {
                        // If we don't know the percentage, just show bytes written
                        print("\rWritten: \(progress.bytesWrittenText)", terminator: "")
                    }

                    // Flush stdout to ensure progress is displayed
                    fflush(stdout)
                }
            }

            // Clear the line and print completion message
            print("\r\u{1B}[K", terminator: "")
            print("✅ Image successfully written to \(drive.name)")

            // Delete the temporary image file
            do {
                try FileManager.default.removeItem(atPath: localImagePath)
                print("🗑️  Temporary image file removed")
            } catch {
                print(
                    "⚠️  Warning: Could not remove temporary image file: \(error.localizedDescription)"
                )
            }

            print("\n🎉 Device \(deviceName) successfully imaged!")
        }
    }
}
