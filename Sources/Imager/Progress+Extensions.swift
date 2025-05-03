import Foundation

extension Progress {
    /// Returns the percentage completion as an optional Double
    public var percentComplete: Double? {
        guard totalUnitCount > 0 else { return nil }
        return Double(completedUnitCount) / Double(totalUnitCount) * 100.0
    }

    /// Returns a formatted string representation of the completed bytes
    public var bytesWrittenText: String {
        return ByteCountFormatter.string(fromByteCount: completedUnitCount, countStyle: .file)
    }

    /// Returns a formatted string representation of the total bytes
    public var totalBytesText: String? {
        guard totalUnitCount > 0 else { return nil }
        return ByteCountFormatter.string(fromByteCount: totalUnitCount, countStyle: .file)
    }

    /// Returns an ASCII progress bar
    /// - Parameters:
    ///   - totalBlocks: The total number of blocks in the progress bar
    ///   - appendPercentageText: Whether to append the percentage text
    /// - Returns: A string representation of the progress bar
    public func asciiProgress(totalBlocks: Int = 30, appendPercentageText: Bool = true) -> String {
        guard let percent = percentComplete else {
            return "[" + String(repeating: " ", count: totalBlocks) + "]"
        }

        let completedBlocks = Int((percent / 100.0) * Double(totalBlocks))
        let remainingBlocks = totalBlocks - completedBlocks

        let progressBar =
            "[" + String(repeating: "=", count: completedBlocks)
            + (completedBlocks < totalBlocks ? ">" : "")
            + String(
                repeating: " ",
                count: max(0, remainingBlocks - (completedBlocks < totalBlocks ? 1 : 0))
            ) + "]"

        if appendPercentageText {
            return "\(progressBar) \(String(format: "%.1f%%", percent))"
        } else {
            return progressBar
        }
    }
}
