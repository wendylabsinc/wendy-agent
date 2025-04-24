import Foundation

/// Represents a physical disk drive in the system.
///
/// This struct encapsulates information about a disk drive, including its identifier,
/// name, capacity, available space, and whether it's an external drive.
///
/// Example:
/// ```swift
/// // An external USB drive with 128GB capacity and 100GB available
/// let usbDrive = Drive(
///     id: "/dev/disk4",
///     name: "SanDisk Ultra",
///     available: 107_374_182_400, // 100GB in bytes
///     capacity: 137_438_953_472,  // 128GB in bytes
///     isExternal: true
/// )
///
/// // An internal system drive with 1TB capacity and 500GB available
/// let systemDrive = Drive(
///     id: "/dev/disk0",
///     name: "Macintosh HD",
///     available: 536_870_912_000, // 500GB in bytes
///     capacity: 1_099_511_627_776, // 1TB in bytes
///     isExternal: false
/// )
/// ```
public struct Drive: Identifiable, Equatable, Hashable, Sendable {
    /// The unique identifier for the drive.
    ///
    /// On macOS, this is typically in the format `/dev/diskN` (e.g., `/dev/disk0`).
    /// On Linux, this is typically in the format `/dev/sdX` (e.g., `/dev/sda`).
    public var id: String
    
    /// The human-readable name of the drive.
    ///
    /// This is typically the volume name if available, or a generic name based on
    /// the drive type (e.g., "Internal Disk", "External Disk").
    public var name: String
    
    /// The available space on the drive in bytes.
    ///
    /// This represents the amount of free space available for writing.
    /// A value of 0 indicates either no available space or that the available space
    /// could not be determined.
    public var available: Int64
    
    /// The total capacity of the drive in bytes.
    ///
    /// This represents the total size of the drive, regardless of how much
    /// is currently in use.
    public var capacity: Int64
    
    /// Indicates whether the drive is an external device.
    ///
    /// `true` if the drive is an external device (e.g., USB drive, SD card),
    /// `false` if it's an internal drive.
    public var isExternal: Bool
    
    /// Returns a human-readable string representation of the available space.
    ///
    /// The string includes both the value and the unit (e.g., "500 GB", "2 TB").
    /// The units are automatically selected based on the size, preferring GB or TB.
    ///
    /// Example:
    /// ```swift
    /// let drive = Drive(id: "/dev/disk1", name: "External SSD", available: 500_000_000_000, capacity: 1_000_000_000_000, isExternal: true)
    /// print(drive.availableHumanReadableText) // Prints "500 GB"
    /// ```
    public var availableHumanReadableText: String {
        // Create a new formatter each time to avoid thread safety issues
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useMB, .useGB, .useTB]
        formatter.countStyle = .file
        formatter.includesUnit = true
        formatter.includesCount = true
        return formatter.string(fromByteCount: available)
    }
    
    /// Returns a human-readable string representation of the total capacity.
    ///
    /// The string includes both the value and the unit (e.g., "1 TB", "128 GB").
    /// The units are automatically selected based on the size, preferring GB or TB.
    ///
    /// Example:
    /// ```swift
    /// let drive = Drive(id: "/dev/disk1", name: "External SSD", available: 500_000_000_000, capacity: 1_000_000_000_000, isExternal: true)
    /// print(drive.capacityHumanReadableText) // Prints "1 TB"
    /// ```
    public var capacityHumanReadableText: String {
        // Create a new formatter each time to avoid thread safety issues
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useMB, .useGB, .useTB]
        formatter.countStyle = .file
        formatter.includesUnit = true
        formatter.includesCount = true
        return formatter.string(fromByteCount: capacity)
    }
}