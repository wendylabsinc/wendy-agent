import Foundation
import Subprocess

/// Linux implementation of the DiskLister protocol.
public struct LinuxDiskLister: DiskLister {

    // MARK: - Codable Structs for lsblk JSON Output

    /// Represents the root JSON structure returned by lsblk -J
    private struct LsblkOutput: Codable {
        let blockdevices: [BlockDevice]
    }

    /// Represents a block device in the lsblk JSON output
    private struct BlockDevice: Codable {
        let name: String
        let model: String?
        let size: String?  // Size comes as a string like "123456789"
        let hotplug: String?  // "0" or "1" as a string
        let children: [BlockDevice]?

        // Computed properties for convenience
        var isExternal: Bool {
            return hotplug == "1"
        }

        var displayName: String {
            return model?.isEmpty == false ? model! : "Linux Disk"
        }

        var sizeInBytes: Int64 {
            return Int64(size ?? "0") ?? 0
        }
    }

    public init() {}

    /// Lists available drives on Linux.
    /// - Parameter all: If true, lists all drives, not just external drives.
    /// - Returns: An array of Drive objects representing the available drives.
    public func list(all: Bool = false) async throws -> [Drive] {
        do {
            // Use lsblk to get information about all block devices in JSON format
            let result = try await Subprocess.run(
                Subprocess.Executable.name("lsblk"),
                arguments: ["-J", "-b", "-o", "NAME,SIZE,MODEL,HOTPLUG,TYPE"],
                output: .string(limit: .max),
                error: .string(limit: .max)
            )

            if result.terminationStatus.isSuccess, let output = result.standardOutput {
                return parseLsblkOutput(output, all: all)
            } else {
                let errorOutput = result.standardError ?? "Unknown error"
                throw NSError(
                    domain: "LinuxDiskLister",
                    code: 1,
                    userInfo: [NSLocalizedDescriptionKey: "Failed to list drives: \(errorOutput)"]
                )
            }
        } catch {
            throw error
        }
    }

    /// Finds a drive by its identifier.
    /// - Parameter id: The identifier of the drive to find.
    /// - Returns: A Drive object representing the found drive.
    /// - Throws: An error if the drive is not found.
    public func findDrive(byId id: String) async throws -> Drive {
        do {
            // Use lsblk to get information about a specific device
            // Normalize the id to remove /dev/ prefix if present
            let deviceId = id.hasPrefix("/dev/") ? String(id.dropFirst(5)) : id

            let result = try await Subprocess.run(
                Subprocess.Executable.name("lsblk"),
                arguments: ["-J", "-b", "-o", "NAME,SIZE,MODEL,HOTPLUG,TYPE", "/dev/\(deviceId)"],
                output: .string(limit: .max),
                error: .string(limit: .max)
            )

            if result.terminationStatus.isSuccess, let output = result.standardOutput {
                let drives = parseLsblkOutput(output, all: true)

                if let drive = drives.first {
                    return drive
                } else {
                    throw NSError(
                        domain: "LinuxDiskLister",
                        code: 2,
                        userInfo: [NSLocalizedDescriptionKey: "Drive not found: \(id)"]
                    )
                }
            } else {
                let errorOutput = result.standardError ?? "Unknown error"
                throw NSError(
                    domain: "LinuxDiskLister",
                    code: 1,
                    userInfo: [NSLocalizedDescriptionKey: "Failed to find drive: \(errorOutput)"]
                )
            }
        } catch {
            throw error
        }
    }

    // MARK: - Private Methods

    /// Parses the JSON output from lsblk.
    /// - Parameters:
    ///   - output: The JSON output from lsblk.
    ///   - all: If true, includes all drives, not just external ones.
    /// - Returns: An array of Drive objects.
    private func parseLsblkOutput(_ output: String, all: Bool) -> [Drive] {
        var drives: [Drive] = []

        // Try to parse the JSON output from lsblk -J
        guard let jsonData = output.data(using: .utf8) else {
            return []
        }

        do {
            // Use JSONDecoder to parse the JSON output
            let decoder = JSONDecoder()
            let lsblkOutput = try decoder.decode(LsblkOutput.self, from: jsonData)

            for device in lsblkOutput.blockdevices {
                // Skip loop devices
                if !device.name.starts(with: "loop") {
                    // Only include external devices unless all is true
                    if all || device.isExternal {
                        let id = "/dev/\(device.name)"

                        let drive = Drive(
                            id: id,
                            name: device.displayName,
                            available: 0,  // Would need df command for accurate values
                            capacity: device.sizeInBytes,
                            isExternal: device.isExternal
                        )

                        drives.append(drive)
                    }
                }
            }
        } catch {
            // Fallback to parsing text output if JSON parsing fails
            return parseTextLsblkOutput(output, all: all)
        }

        return drives
    }

    // Fallback parser for non-JSON lsblk output
    private func parseTextLsblkOutput(_ output: String, all: Bool) -> [Drive] {
        var drives: [Drive] = []

        // Split the output by lines
        let lines = output.split(separator: "\n")

        // Skip the header line
        for i in 1..<lines.count {
            let line = lines[i]
            let components = line.split(separator: " ").map {
                $0.trimmingCharacters(in: .whitespacesAndNewlines)
            }

            // Basic parsing of lsblk text output
            if components.count >= 2 {
                let name = components[0]

                // Skip loop devices and partitions
                if !name.starts(with: "loop") && !name.contains("├") && !name.contains("└") {
                    let id = "/dev/\(name)"
                    let displayName = components.count > 2 ? components[2] : "Linux Disk"

                    // Size is in bytes (assuming it's the second column)
                    let capacity = Int64(components[1]) ?? 0

                    // Assume all devices are external in fallback mode if not filtering
                    let isExternal = true

                    if all || isExternal {
                        let drive = Drive(
                            id: id,
                            name: displayName.isEmpty ? "Linux Disk" : displayName,
                            available: 0,
                            capacity: capacity,
                            isExternal: isExternal
                        )

                        drives.append(drive)
                    }
                }
            }
        }

        return drives
    }
}
