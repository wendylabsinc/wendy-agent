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
        
        // Create a task to run the dd command
        do {
            // Capture progressHandler in a local variable to avoid data races
            let localProgressHandler = progressHandler
            
            let result = try await Subprocess.run(
                Subprocess.Executable.name("sudo"),
                arguments: ["dd", "if=\(imagePath)", "of=\(drive.id)", "bs=1m", "status=progress"],
                output: .sequence,
                error: .sequence
            ) { execution in
                // Process standard output for progress updates
                for try await chunk in execution.standardOutput {
                    // Try to parse dd output for progress
                    let outputString = chunk.withUnsafeBytes { 
                        String(decoding: $0, as: UTF8.self) 
                    }
                    
                    // dd outputs progress like: "1234567890 bytes (1.2 GB, 1.1 GiB) copied, 10 s, 123 MB/s"
                    let pattern = #"(\d+)\s+bytes"#
                    if let range = outputString.range(of: pattern, options: .regularExpression),
                       let bytes = Int64(outputString[range].split(separator: " ")[0]) {
                        
                        let progress = DiskWriteProgress(
                            bytesWritten: bytes,
                            totalBytes: totalBytes
                        )
                        
                        localProgressHandler(progress)
                    }
                }
                
                // Process standard error for progress updates (dd sometimes writes to stderr)
                for try await chunk in execution.standardError {
                    // Similar parsing as above
                    let errorString = chunk.withUnsafeBytes { 
                        String(decoding: $0, as: UTF8.self) 
                    }
                    
                    let pattern = #"(\d+)\s+bytes"#
                    if let range = errorString.range(of: pattern, options: .regularExpression),
                       let bytes = Int64(errorString[range].split(separator: " ")[0]) {
                        
                        let progress = DiskWriteProgress(
                            bytesWritten: bytes,
                            totalBytes: totalBytes
                        )
                        
                        localProgressHandler(progress)
                    }
                }
                
                return execution
            }
            
            // If we get here, the command completed successfully
            if !result.terminationStatus.isSuccess {
                throw DiskWriterError.writeFailed(reason: "dd command failed with status: \(result.terminationStatus)")
            }
        } catch {
            throw DiskWriterError.writeFailed(reason: error.localizedDescription)
        }
    }
}