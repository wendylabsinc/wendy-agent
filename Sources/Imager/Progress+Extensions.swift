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

}
