import Foundation
import Subprocess

/// A disk writer implementation for macOS that uses the `dd` command.
public class MacOSDiskWriter: DiskWriter {
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
        let totalBytes = try? FileManager.default.attributesOfItem(atPath: imagePath)[.size] as? Int64
        
        // Send initial progress update
        progressHandler(DiskWriteProgress(bytesWritten: 0, totalBytes: totalBytes))
        
        do {
            // First, unmount the disk to ensure it's not busy
            print("Unmounting disk \(drive.id)...")
            let unmountResult = try await Subprocess.run(
                Subprocess.Executable.name("sudo"),
                arguments: ["diskutil", "unmountDisk", drive.id],
                output: .string,
                error: .string
            )
            
            if !unmountResult.terminationStatus.isSuccess {
                if let errorOutput = unmountResult.standardError, !errorOutput.isEmpty {
                    throw DiskWriterError.writeFailed(reason: "Failed to unmount disk: \(errorOutput)")
                } else {
                    throw DiskWriterError.writeFailed(reason: "Failed to unmount disk with status: \(unmountResult.terminationStatus)")
                }
            }
            
            // Run the dd command with a custom script that sends progress updates
            print("Writing image to \(drive.id)...")
            
            // Create a bash script that runs dd and sends SIGINFO to it periodically
            let script = """
            dd if="\(imagePath)" of="\(drive.id)" bs=1m status=progress 2>&1 & DD_PID=$!
            while kill -0 $DD_PID 2>/dev/null; do
                kill -INFO $DD_PID 2>/dev/null
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
                arguments: ["bash", "-c", script],
                output: .sequence,
                error: .discarded
            ) { execution in
                // Process standard output for progress updates
                for try await chunk in execution.standardOutput {
                    // Convert the chunk to a string
                    let outputString = chunk.withUnsafeBytes { 
                        String(decoding: $0, as: UTF8.self) 
                    }
                    
                    // Parse the progress information
                    // dd outputs progress like: "1234567890 bytes (1.2 GB, 1.1 GiB) copied, 10 s, 123 MB/s"
                    let pattern = #"(\d+)\s+bytes"#
                    if let range = outputString.range(of: pattern, options: .regularExpression),
                       let bytes = Int64(outputString[range].split(separator: " ")[0]) {
                        
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
                throw DiskWriterError.writeFailed(reason: "dd command failed with status: \(result.terminationStatus)")
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
            
            print("Image successfully written to \(drive.id)")
            print("✅ Image successfully written to \(drive.name)")
        } catch let error as DiskWriterError {
            // Re-throw DiskWriterError
            throw error
        } catch {
            // Convert other errors to DiskWriterError with detailed message
            print("Error details: \(error)")
            throw DiskWriterError.writeFailed(reason: "Error: \(error.localizedDescription)")
        }
    }
}