import Foundation
import Subprocess

#if os(Linux)
    /// A disk writer implementation for Linux that uses the `dd` command.
    public class LinuxDiskWriter: DiskWriter {
        public init() {}

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

            // Get image file size to track total progress
            let totalBytes =
                try? FileManager.default.attributesOfItem(atPath: imagePath)[.size] as? Int64

            // Send initial progress update
            progressHandler(DiskWriteProgress(bytesWritten: 0, totalBytes: totalBytes))

            do {
                // First, unmount the disk to ensure it's not busy
                // On Linux, we use umount command
                let unmountResult = try await Subprocess.run(
                    Subprocess.Executable.name("sudo"),
                    arguments: ["umount", drive.id],
                    output: .string(limit: .max),
                    error: .string
                )

                // On Linux, umount may fail if the drive is not mounted, which is fine for our purposes
                // We only care if there's an actual error that would prevent writing
                // Note: We ignore the specific exit code and just check if it's successful or not
                if !unmountResult.terminationStatus.isSuccess {
                    // Check if there's an error message that indicates a real problem
                    if let errorOutput = unmountResult.standardError,
                        !errorOutput.isEmpty && !errorOutput.contains("not mounted")
                    {
                        throw DiskWriterError.writeFailed(
                            reason: "Failed to unmount disk: \(errorOutput)"
                        )
                    }
                    // Otherwise, we assume it's just not mounted, which is fine
                }

                // Create a bash script that runs dd and sends SIGUSR1 to it periodically (Linux equivalent of SIGINFO)
                let script = """
                    dd if="\(imagePath)" of="\(drive.id)" bs=1M status=progress 2>&1 & DD_PID=$!
                    while kill -0 $DD_PID 2>/dev/null; do
                        kill -USR1 $DD_PID 2>/dev/null
                        sleep 1
                    done
                    wait $DD_PID
                    exit $?
                    """

                // Store the progress handler in a local variable to avoid capturing it in the closure
                let localProgressHandler = progressHandler
                let localTotalBytes = totalBytes

                // Use the Subprocess API with a closure to capture output in real-time
                let result = try await Subprocess.run(
                    Subprocess.Executable.name("sudo"),
                    arguments: ["bash", "-c", script]
                ) { execution, stdin, stdout, strerr in
                    // Process standard output for progress updates
                    for try await chunk in stdout {
                        // Convert the chunk to a string
                        let outputString = chunk.withUnsafeBytes {
                            String(decoding: $0, as: UTF8.self)
                        }

                        // Parse the progress information
                        // dd on Linux outputs progress like: "1234567890 bytes (1.2 GB, 1.1 GiB) copied, 10 s, 123 MB/s"
                        let pattern = #"(\d+)\s+bytes"#
                        if let range = outputString.range(of: pattern, options: .regularExpression),
                            let bytes = Int64(outputString[range].split(separator: " ")[0])
                        {

                            let progress = DiskWriteProgress(
                                bytesWritten: bytes,
                                totalBytes: localTotalBytes
                            )

                            localProgressHandler(progress)
                        }
                    }

                    return execution
                }

                // Check if the command was successful
                if !result.terminationStatus.isSuccess {
                    throw DiskWriterError.writeFailed(
                        reason: "dd command failed with status: \(result.terminationStatus)"
                    )
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
#else
    // Empty implementation for non-Linux platforms
    public class LinuxDiskWriter: DiskWriter {
        public init() {}

        public func write(
            imagePath: String,
            drive: Drive,
            progressHandler: @escaping (DiskWriteProgress) -> Void
        ) async throws {
            fatalError("LinuxDiskWriter is only available on Linux platforms")
        }
    }
#endif
