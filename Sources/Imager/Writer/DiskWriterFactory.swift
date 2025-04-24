import Foundation

/// Factory for creating platform-specific DiskWriter implementations.
public enum DiskWriterFactory {
    
    /// Creates a DiskWriter instance appropriate for the current platform.
    /// - Returns: A DiskWriter implementation for the current platform.
    public static func createDiskWriter() -> DiskWriter {
        #if os(macOS)
        return MacOSDiskWriter()
        #elseif os(Linux)
        return LinuxDiskWriter()
        #else
        // Default to macOS implementation for other platforms
        return MacOSDiskWriter()
        #endif
    }
}