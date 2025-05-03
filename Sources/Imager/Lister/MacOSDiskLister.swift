import Foundation
import Subprocess

/// macOS implementation of the DiskLister protocol.
public struct MacOSDiskLister: DiskLister {

    public init() {}

    /// Lists available external drives.
    /// - Parameter all: If true, lists all drives, not just external drives.
    /// - Returns: An array of Drive objects representing the available drives.
    public func list(all: Bool) async throws -> [Drive] {
        do {
            let result = try await Subprocess.run(
                Subprocess.Executable.name("diskutil"),
                arguments: all ? ["list"] : ["list", "external"],
                output: .string,
                error: .string
            )

            if result.terminationStatus.isSuccess {
                if let output = result.standardOutput, !output.isEmpty {
                    // Parse the output into Drive objects
                    return parseDiskUtilOutput(output, all: all)
                } else {
                    return []
                }
            } else {
                if result.standardError != nil {
                    // Error occurred, but we're not using the error output
                }
                return []
            }
        } catch {
            throw error
        }
    }

    /// Finds a drive by its identifier.
    /// - Parameter id: The identifier of the drive to find.
    /// - Returns: The Drive object if found.
    /// - Throws: If the drive cannot be found.
    public func findDrive(byId id: String) async throws -> Drive {
        do {
            let result = try await Subprocess.run(
                Subprocess.Executable.name("diskutil"),
                arguments: ["info", id],
                output: .string,
                error: .string
            )

            if result.terminationStatus.isSuccess {
                if let output = result.standardOutput, !output.isEmpty {
                    // Parse the output to get drive information
                    if let drive = try await parseDiskUtilInfoOutput(output, id: id) {
                        return drive
                    } else {
                        throw DiskListerError.driveNotFound(id: id)
                    }
                } else {
                    throw DiskListerError.driveNotFound(id: id)
                }
            } else {
                if result.standardError != nil {
                    // Error occurred, but we're not using the error output
                }
                throw DiskListerError.driveNotFound(id: id)
            }
        } catch {
            throw error
        }
    }

    private func parseDiskUtilInfoOutput(_ output: String, id: String) async throws -> Drive? {
        // Extract drive information from diskutil info output
        var name = "Unknown"
        var capacity: Int64 = 0
        var available: Int64 = 0
        var isExternal = false

        // Parse name
        if let nameRange = output.range(of: "Device / Media Name:.*", options: .regularExpression) {
            let nameLine = String(output[nameRange])
            if let nameValue = nameLine.split(separator: ":").last?.trimmingCharacters(
                in: .whitespacesAndNewlines
            ) {
                name = nameValue
            }
        }

        // Parse capacity
        if let sizeRange = output.range(of: "Disk Size:.*", options: .regularExpression) {
            let sizeLine = String(output[sizeRange])
            if let sizeString = sizeLine.split(separator: "(").first?.trimmingCharacters(
                in: .whitespacesAndNewlines
            ),
                let sizeValue = sizeString.split(separator: ":").last?.trimmingCharacters(
                    in: .whitespacesAndNewlines
                )
            {
                // Extract numeric value from string like "500.3 GB"
                let numericPart = sizeValue.components(
                    separatedBy: CharacterSet.decimalDigits.inverted
                ).joined()
                if let sizeNumber = Int64(numericPart) {
                    capacity = sizeNumber * 1_000_000  // Approximate conversion to bytes
                }
            }
        }

        // Check if external
        if let locationRange = output.range(of: "Device Location:.*", options: .regularExpression) {
            let locationLine = String(output[locationRange])
            isExternal = locationLine.contains("External")
        }

        // For available space, we'll need to use df command
        do {
            let dfResult = try await Subprocess.run(
                Subprocess.Executable.name("df"),
                arguments: ["-k", id],
                output: .string,
                error: .string
            )

            if dfResult.terminationStatus.isSuccess,
                let dfOutput = dfResult.standardOutput
            {
                let lines = dfOutput.split(separator: "\n")
                if lines.count > 1 {
                    let parts = lines[1].split(separator: " ").filter { !$0.isEmpty }
                    if parts.count >= 4, let availableKB = Int64(parts[3]) {
                        available = availableKB * 1024  // Convert KB to bytes
                    }
                }
            }
        } catch {
        }

        return Drive(
            id: id,
            name: name,
            available: available,
            capacity: capacity,
            isExternal: isExternal
        )
    }

    private func parseDiskUtilOutput(_ output: String, all: Bool) -> [Drive] {
        var drives: [Drive] = []

        // Split the output by lines
        let lines = output.split(separator: "\n")

        // Variables to track the current disk being processed
        var currentDiskId: String?
        var currentDiskName: String?
        var currentDiskSize: Int64 = 0
        var freeSpace: Int64 = 0
        var isExternal: Bool = false

        for line in lines {
            let trimmedLine = line.trimmingCharacters(in: .whitespacesAndNewlines)

            // Check if this is a disk header line
            if trimmedLine.contains("(external, physical)")
                || trimmedLine.contains("(internal, physical)")
            {
                // If we were processing a disk, add it to the list
                if let id = currentDiskId, let name = currentDiskName, currentDiskSize > 0 {
                    // Only add the drive if we want all drives or if it's external
                    if all || isExternal {
                        let drive = Drive(
                            id: id,
                            name: name,
                            available: freeSpace,
                            capacity: currentDiskSize,
                            isExternal: isExternal
                        )
                        drives.append(drive)
                    }
                }

                // Start tracking a new disk
                let components = trimmedLine.split(separator: " ")
                currentDiskId = String(components[0])
                currentDiskName =
                    trimmedLine.contains("(external, physical)") ? "External Disk" : "Internal Disk"
                // Default name
                currentDiskSize = 0
                freeSpace = 0
                isExternal = trimmedLine.contains("(external, physical)")
            }
            // Check if this is a size line
            else if trimmedLine.contains("*")
                && (trimmedLine.contains("GB") || trimmedLine.contains("TB"))
            {
                // Extract the size information
                let sizeComponents = trimmedLine.split(separator: "*")
                if sizeComponents.count > 1 {
                    let sizeString = sizeComponents[1].split(separator: " ")[0]
                    if let size = Double(sizeString) {
                        // Check if it's TB or GB
                        if trimmedLine.contains("TB") {
                            // Convert TB to bytes
                            currentDiskSize = Int64(size * 1_000_000_000_000)
                        } else {
                            // Convert GB to bytes
                            currentDiskSize = Int64(size * 1_000_000_000)
                        }
                    }
                }
            }
            // Check if this is a free space line
            else if trimmedLine.contains("(free space)") {
                let components = trimmedLine.split(separator: " ")
                for (index, component) in components.enumerated() {
                    if (component.contains("GB") || component.contains("TB")) && index > 0 {
                        if let freeSize = Double(components[index - 1]) {
                            if component.contains("TB") {
                                // Convert TB to bytes
                                freeSpace = Int64(freeSize * 1_000_000_000_000)
                            } else {
                                // Convert GB to bytes
                                freeSpace = Int64(freeSize * 1_000_000_000)
                            }
                        }
                        break
                    }
                }
            }
            // Check if this is a name line
            else if trimmedLine.contains("NAME") {
                // Skip the header line
                continue
            }
            // Check if this is a partition line with a name
            else if trimmedLine.contains("TYPE") && trimmedLine.contains("NAME") {
                let components = trimmedLine.split(separator: " ")
                for (index, component) in components.enumerated() {
                    if component == "NAME" && index + 1 < components.count {
                        currentDiskName = String(components[index + 1])
                        break
                    }
                }
            }
        }

        // Add the last disk if we were processing one
        if let id = currentDiskId, let name = currentDiskName, currentDiskSize > 0 {
            // Only add the drive if we want all drives or if it's external
            if all || isExternal {
                let drive = Drive(
                    id: id,
                    name: name,
                    available: freeSpace,
                    capacity: currentDiskSize,
                    isExternal: isExternal
                )
                drives.append(drive)
            }
        }

        return drives
    }
}
