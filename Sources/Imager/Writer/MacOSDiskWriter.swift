#if os(macOS)
    import Foundation
    import Logging
    import Subprocess

    /// A disk writer implementation for macOS that uses the `dd` command.
    public class MacOSDiskWriter: DiskWriter {
        private let logger: Logger

        public init(logger: Logger = Logger(label: "sh.wendy.imager.macos")) {
            self.logger = logger
        }

        public func write(
            imagePath: String,
            drive: Drive,
            progressHandler: @escaping (DiskWriteProgress) -> Void
        ) async throws {
            // Check if image exists
            guard FileManager.default.fileExists(atPath: imagePath) else {
                throw DiskWriterError.imageNotFoundInPath(path: imagePath)
            }

            // Check if image is a .img file
            guard imagePath.hasSuffix(".img") else {
                throw DiskWriterError.imageFileIncorrectType
            }

            // Correctly determine the image file size as Int64. `FileManager` returns `NSNumber`,
            // so we need to bridge it instead of casting directly to `Int64`.
            let totalBytes: Int64?
            do {
                let attributes = try FileManager.default.attributesOfItem(atPath: imagePath)
                if let fileSizeNumber = attributes[.size] as? NSNumber {
                    totalBytes = fileSizeNumber.int64Value
                } else if let fileSize = attributes[.size] as? Int {
                    totalBytes = Int64(fileSize)
                } else {
                    totalBytes = nil
                }
            } catch {
                totalBytes = nil
            }

            // Send initial progress update
            progressHandler(DiskWriteProgress(bytesWritten: 0, totalBytes: totalBytes))

            // Ensure drive ID is properly formatted with /dev/ prefix
            let devicePath: String
            if drive.id.hasPrefix("/dev/") {
                devicePath = drive.id
            } else {
                devicePath = "/dev/\(drive.id)"
            }

            // Use raw disk device for faster access
            let rawDevicePath = devicePath.replacingOccurrences(of: "/dev/disk", with: "/dev/rdisk")

            do {
                // First, unmount the disk to ensure it's not busy
                let unmountResult = try await Subprocess.run(
                    Subprocess.Executable.name("sudo"),
                    arguments: ["diskutil", "unmountDisk", devicePath],
                    output: .string(limit: .max),
                    error: .string(limit: .max)
                )

                if !unmountResult.terminationStatus.isSuccess {
                    if let errorOutput = unmountResult.standardError, !errorOutput.isEmpty {
                        throw DiskWriterError.writeFailed(
                            reason: "Failed to unmount disk: \(errorOutput)"
                        )
                    } else {
                        throw DiskWriterError.writeFailed(
                            reason:
                                "Failed to unmount disk with status: \(unmountResult.terminationStatus)"
                        )
                    }
                }

                // Create a bash script that uses pv for real progress
                let script: String

                // Check if pv is available
                let pvCheckResult = try await Subprocess.run(
                    Subprocess.Executable.name("which"),
                    arguments: ["pv"],
                    output: .string(limit: .max),
                    error: .discarded
                )

                if pvCheckResult.terminationStatus.isSuccess {
                    // Use pv for progress - it outputs progress info to stderr
                    script = """
                        #!/bin/bash

                        # Use pv to show progress while piping to dd
                        # -p: show progress bar
                        # -t: show elapsed time
                        # -e: show ETA
                        # -r: show rate
                        # -b: show total bytes transferred
                        # -s: set expected size for percentage calculation
                        # -f: force output even if not to terminal
                        pv -fperb -s \(totalBytes ?? 0) "\(imagePath)" | dd of="\(rawDevicePath)" bs=1m
                        """
                } else {
                    // Fallback to plain dd
                    script = """
                        #!/bin/bash

                        # Run dd without progress
                        dd if="\(imagePath)" of="\(rawDevicePath)" bs=1m 2>&1
                        """
                }

                if pvCheckResult.terminationStatus.isSuccess {
                    // Use pv and let it handle all progress display

                    // Run pv + dd command with stderr passthrough
                    let pvResult = try await Subprocess.run(
                        Subprocess.Executable.name("sudo"),
                        arguments: ["bash", "-c", script],
                        output: .discarded,
                        error: .fileDescriptor(.standardError, closeAfterSpawningProcess: false)
                    )

                    // Check result
                    if !pvResult.terminationStatus.isSuccess {
                        throw DiskWriterError.writeFailed(
                            reason: "dd command failed with status: \(pvResult.terminationStatus)"
                        )
                    }
                } else {
                    // Fallback for when pv is not available - use timer for estimation
                    let startTime = Date()
                    var lastProgress: Int64 = 0

                    // Create a timer using DispatchQueue for progress updates
                    let progressQueue = DispatchQueue(label: "diskwriter.progress")
                    let timer = DispatchSource.makeTimerSource(queue: progressQueue)
                    timer.schedule(deadline: .now(), repeating: 1.0)

                    timer.setEventHandler { [weak timer] in
                        guard timer != nil else { return }

                        let elapsed = Date().timeIntervalSince(startTime)
                        if elapsed > 0, let totalBytes = totalBytes, totalBytes > 0 {
                            // Estimate progress based on typical write speeds
                            let speed: Int64
                            if elapsed < 5 {
                                speed = 20 * 1024 * 1024  // 20 MB/s initially
                            } else if elapsed < 10 {
                                speed = 30 * 1024 * 1024  // 30 MB/s
                            } else {
                                speed = 35 * 1024 * 1024  // 35 MB/s steady
                            }

                            var estimatedBytes = Int64(elapsed) * speed
                            let maxBytes = (totalBytes * 95) / 100
                            if estimatedBytes > maxBytes {
                                estimatedBytes = maxBytes
                            }

                            if estimatedBytes > lastProgress {
                                lastProgress = estimatedBytes
                                progressHandler(
                                    DiskWriteProgress(
                                        bytesWritten: estimatedBytes,
                                        totalBytes: totalBytes
                                    )
                                )
                            }
                        }
                    }

                    timer.resume()

                    // Run plain dd command
                    let ddResult = try await Subprocess.run(
                        Subprocess.Executable.name("sudo"),
                        arguments: ["bash", "-c", script],
                        output: .discarded,
                        error: .discarded
                    )

                    timer.cancel()

                    // Check result
                    if !ddResult.terminationStatus.isSuccess {
                        throw DiskWriterError.writeFailed(
                            reason: "dd command failed with status: \(ddResult.terminationStatus)"
                        )
                    }
                }

                // If we get here, the command completed successfully
                // Send a final progress update showing 100% completion
                if let totalBytes = totalBytes {
                    // Ensure we show exactly 100% by setting bytesWritten = totalBytes
                    let finalProgress = DiskWriteProgress(
                        bytesWritten: totalBytes,
                        totalBytes: totalBytes
                    )
                    progressHandler(finalProgress)
                }
            } catch let error as DiskWriterError {
                // Re-throw DiskWriterError
                throw error
            } catch {
                // Convert other errors to DiskWriterError with detailed message
                throw DiskWriterError.writeFailed(reason: "Error: \(error.localizedDescription)")
            }
        }
    }
#endif
