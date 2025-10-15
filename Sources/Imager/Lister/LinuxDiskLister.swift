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
        let type: String?  // "disk", "part", "loop", "rom", etc.
        let fsavail: String?  // Available filesystem space in bytes
        let fsused: String?  // Used filesystem space in bytes
        let fssize: String?  // Total filesystem size in bytes
        let mountpoint: String?  // Mount point of the filesystem
        let children: [BlockDevice]?

        // Computed properties for convenience
        var isExternal: Bool {
            return hotplug == "1"
        }

        var isDisk: Bool {
            // Only consider actual disks and ROM drives (CD/DVD)
            return type == "disk" || type == "rom"
        }

        var displayName: String {
            if let model = model, !model.isEmpty {
                return model
            }
            // Provide better default names based on device type
            switch type {
            case "rom":
                return "Optical Drive (\(name))"
            case "disk":
                return "Disk (\(name))"
            default:
                return "Storage Device (\(name))"
            }
        }

        var sizeInBytes: Int64 {
            return Int64(size ?? "0") ?? 0
        }

        var availableBytes: Int64 {
            // Try to get available space from filesystem info
            if let fsavail = fsavail, let availBytes = Int64(fsavail) {
                return availBytes
            }

            // Check children partitions for available space (sum of all partitions)
            if let children = children {
                let totalAvailable = children.reduce(Int64(0)) { sum, child in
                    if let fsavail = child.fsavail, let availBytes = Int64(fsavail) {
                        return sum + availBytes
                    }
                    return sum
                }
                if totalAvailable > 0 {
                    return totalAvailable
                }
            }

            // If unmounted and no filesystem info, assume entire disk is available
            if mountpoint == nil || mountpoint?.isEmpty == true {
                return sizeInBytes
            }

            // Default to 0 if we can't determine
            return 0
        }
    }

    public init() {}

    /// Lists available drives on Linux.
    /// - Parameter all: If true, lists all drives, not just external drives.
    /// - Returns: An array of Drive objects representing the available drives.
    public func list(all: Bool = false) async throws -> [Drive] {
        do {
            // Use lsblk to get information about all block devices in JSON format
            // Include filesystem information for available space calculation
            let result = try await Subprocess.run(
                Subprocess.Executable.name("lsblk"),
                arguments: ["-J", "-b", "-o", "NAME,SIZE,MODEL,HOTPLUG,TYPE,FSAVAIL,FSUSED,FSSIZE,MOUNTPOINT"],
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
        // Validate input to prevent injection or invalid paths
        guard !id.isEmpty else {
            throw NSError(
                domain: "LinuxDiskLister",
                code: 3,
                userInfo: [NSLocalizedDescriptionKey: "Drive ID cannot be empty"]
            )
        }

        // Normalize the id to remove /dev/ prefix if present
        let deviceId = id.hasPrefix("/dev/") ? String(id.dropFirst(5)) : id

        // Further validate the device ID - it should be alphanumeric with possible numbers
        let validPattern = "^[a-zA-Z]+[a-zA-Z0-9]*$"
        guard deviceId.range(of: validPattern, options: .regularExpression) != nil else {
            throw NSError(
                domain: "LinuxDiskLister",
                code: 4,
                userInfo: [NSLocalizedDescriptionKey: "Invalid drive ID format: \(id)"]
            )
        }

        let devicePath = "/dev/\(deviceId)"

        // First, check if the device exists
        if !FileManager.default.fileExists(atPath: devicePath) {
            throw NSError(
                domain: "LinuxDiskLister",
                code: 5,
                userInfo: [NSLocalizedDescriptionKey: "Device does not exist: \(devicePath)"]
            )
        }

        do {
            let result = try await Subprocess.run(
                Subprocess.Executable.name("lsblk"),
                arguments: ["-J", "-b", "-o", "NAME,SIZE,MODEL,HOTPLUG,TYPE,FSAVAIL,FSUSED,FSSIZE,MOUNTPOINT", devicePath],
                output: .string(limit: .max),
                error: .string(limit: .max)
            )

            if result.terminationStatus.isSuccess, let output = result.standardOutput {
                let drives = parseLsblkOutput(output, all: true)

                if let drive = drives.first {
                    // Ensure the drive ID is correctly formatted
                    if !drive.id.hasPrefix("/dev/") {
                        // Fix the drive ID if needed
                        var correctedDrive = drive
                        correctedDrive.id = devicePath
                        return correctedDrive
                    }
                    return drive
                } else {
                    throw NSError(
                        domain: "LinuxDiskLister",
                        code: 2,
                        userInfo: [NSLocalizedDescriptionKey: "Drive not found or not a valid disk: \(id)"]
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
                // Only include actual disk devices (not partitions, loops, etc.)
                if device.isDisk {
                    // Only include external devices unless all is true
                    if all || device.isExternal {
                        let id = "/dev/\(device.name)"

                        let drive = Drive(
                            id: id,
                            name: device.displayName,
                            available: device.availableBytes,
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
        print("Warning: Failed to parse lsblk JSON output. Disk listing may be incomplete.")

        // Try a simpler approach: run lsblk without JSON but with better formatting
        // This is a synchronous fallback, not ideal but better than broken parsing
        do {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/usr/bin/lsblk")
            process.arguments = ["-rno", "NAME,SIZE,TYPE,MODEL,HOTPLUG,FSAVAIL"]  // -r for raw, -n for no headers

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = Pipe()

            try process.run()
            process.waitUntilExit()

            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            guard let output = String(data: data, encoding: .utf8) else { return [] }

            var drives: [Drive] = []
            let lines = output.split(separator: "\n")

            for line in lines {
                let components = line.split(separator: " ", maxSplits: 5).map { String($0) }
                if components.count >= 3 {
                    let name = components[0]
                    let size = Int64(components[1]) ?? 0
                    let type = components[2]
                    let model = components.count > 3 ? components[3] : ""
                    let hotplug = components.count > 4 ? components[4] : "0"
                    let fsavail = components.count > 5 ? Int64(components[5]) : nil

                    // Only include actual disks
                    if type == "disk" || type == "rom" {
                        let isExternal = hotplug == "1"
                        if all || isExternal {
                            let displayName = !model.isEmpty ? model : "Disk (\(name))"
                            // Use fsavail if available, otherwise assume entire disk is available if unmounted
                            let available = fsavail ?? size
                            let drive = Drive(
                                id: "/dev/\(name)",
                                name: displayName,
                                available: available,
                                capacity: size,
                                isExternal: isExternal
                            )
                            drives.append(drive)
                        }
                    }
                }
            }
            return drives
        } catch {
            print("Error running fallback lsblk: \(error)")
            return []
        }
    }
}
