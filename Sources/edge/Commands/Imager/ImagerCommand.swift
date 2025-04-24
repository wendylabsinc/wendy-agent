import ArgumentParser
import Imager
import Foundation
#if os(macOS)
import Darwin
#elseif os(Linux)
import Glibc
#endif

struct ImagerCommand: AsyncParsableCommand {

    static let configuration = CommandConfiguration(
        commandName: "imager",
        abstract: "Image EdgeOS projects.",
        subcommands: [ListCommand.self, WriteCommand.self]
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
                        let progressBar = progress.asciiProgress(totalBlocks: 30, appendPercentageText: false)
                        
                        // Print progress with percentage and written/total bytes
                        if let totalText = progress.totalBytesText {
                            print("\r\(progressBar) \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)/\(totalText)", terminator: "")
                        } else {
                            print("\r\(progressBar) \(String(format: "%.1f%%", percent)) - \(progress.bytesWrittenText)", terminator: "")
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
}