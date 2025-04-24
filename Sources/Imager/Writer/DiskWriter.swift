import Foundation

/// Progress information for disk writing
public struct DiskWriteProgress {
    /// Number of bytes written so far
    public let bytesWritten: Int64
    
    /// Total bytes to write (if known)
    public let totalBytes: Int64?
    
    /// Estimated percentage complete (0-100)
    public var percentComplete: Double? {
        guard let total = totalBytes, total > 0 else { return nil }
        return min(100.0, Double(bytesWritten) / Double(total) * 100.0)
    }
    
    /// Human-readable representation of bytes written
    public var bytesWrittenText: String {
        // Use a synchronized approach to access the formatter
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useKB, .useMB, .useGB]
        formatter.countStyle = .file
        return formatter.string(fromByteCount: bytesWritten)
    }
    
    /// Human-readable representation of total bytes
    public var totalBytesText: String? {
        guard let total = totalBytes else { return nil }
        // Use a synchronized approach to access the formatter
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useKB, .useMB, .useGB]
        formatter.countStyle = .file
        return formatter.string(fromByteCount: total)
    }

    /// Returns an ASCII progress bar string
    /// `[██████████▒▒▒▒▒▒▒▒] 50.0%`
    /// Uses ▒ for incomplete blocks
    /// Uses █ for completed blocks
    /// - Parameters:
    ///   - totalBlocks: Total number of blocks of ascii characters to complete excluding brackets  
    ///   - appendPercentageText: Whether to append the percentage text
    /// - Returns: A string representing the progress bar
    public func asciiProgress(totalBlocks: Int = 20, appendPercentageText: Bool = true) -> String {
        guard let percent = percentComplete else { return "" }
        
        // Special case: if we're at 100%, fill all blocks
        if percent >= 100.0 {
            var progress = "[" + String(repeating: "█", count: totalBlocks) + "]"
            if appendPercentageText {
                progress += " \(String(format: "%.1f%%", percent))"
            }
            return progress
        }
        
        let completedBlocks = Int(Double(totalBlocks) * percent / 100.0)
        let remainingBlocks = totalBlocks - completedBlocks
        var progress = "[" + String(repeating: "█", count: completedBlocks) + 
               String(repeating: "▒", count: remainingBlocks) + "]"
        if appendPercentageText {
            progress += " \(String(format: "%.1f%%", percent))"
        }
        return progress
    }
}

/// Errors that can occur during disk writing operations
public enum DiskWriterError: Error {
    /// Image not found in path
    case imageNotFoundInPath(path: String)
    /// Only .img files are supported
    case imageFileIncorrectType
    /// Failed to write image to drive
    case writeFailed(reason: String)

    public var description: String {
        switch self {
        case .imageNotFoundInPath(let path):
            return "Image not found in path: \(path)"
        case .imageFileIncorrectType:
            return "Image file is not a valid image file. Only .img files are supported."
        case .writeFailed(let reason):
            return "Write failed: \(reason)"
        }
    }
}

public protocol DiskWriter {
    /// Write an image file to a drive with progress reporting
    /// - Parameters:
    ///   - imagePath: Path to the image file to write
    ///   - drive: The target drive to write to
    ///   - progressHandler: Callback that will be called periodically with progress updates
    /// - Throws: If the write operation fails
    func write(
        imagePath: String, 
        drive: Drive, 
        progressHandler: @escaping (DiskWriteProgress) -> Void
    ) async throws
}