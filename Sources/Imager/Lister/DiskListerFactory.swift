import Foundation

/// Factory for creating platform-specific DiskLister implementations.
public enum DiskListerFactory {

    /// Creates a DiskLister instance appropriate for the current platform.
    /// - Returns: A DiskLister implementation for the current platform.
    public static func createDiskLister() -> DiskLister {
        #if os(macOS)
            return MacOSDiskLister()
        #elseif os(Linux)
            return LinuxDiskLister()
        #else
            // Default to macOS implementation for other platforms
            return MacOSDiskLister()
        #endif
    }
}
