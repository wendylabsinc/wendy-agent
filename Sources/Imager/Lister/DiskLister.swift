import Foundation

/// Errors that can occur during disk listing operations.
public enum DiskListerError: Error {
    case driveNotFound(id: String, error: String)
    case commandFailed(error: Error)
    case listFailed(error: String)
    case unknownOutput
}

/// Protocol defining the interface for disk listing functionality.
public protocol DiskLister {
    /// Lists available drives.
    /// - Parameter all: If true, lists all drives, not just external drives.
    /// - Returns: An array of Drive objects representing the available drives.
    func list(all: Bool) async throws -> [Drive]

    /// Find a drive by its ID
    /// - Parameter id: The ID of the drive to find
    /// - Returns: The Drive object if found
    /// - Throws: If the drive cannot be found
    func findDrive(byId id: String) async throws -> Drive
}
