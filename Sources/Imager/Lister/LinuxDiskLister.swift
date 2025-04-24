import Foundation
import Subprocess

/// Linux implementation of the DiskLister protocol.
public struct LinuxDiskLister: DiskLister {
    
    public init() {}
    
    /// Lists available drives on Linux.
    /// - Parameter all: If true, lists all drives, not just external drives.
    /// - Returns: An array of Drive objects representing the available drives.
    public func list(all: Bool) async throws -> [Drive] {
        do {
            // Use lsblk command to list block devices on Linux
            let result = try await Subprocess.run(
                Subprocess.Executable.name("lsblk"),
                arguments: ["-b", "-J", "-o", "NAME,SIZE,MOUNTPOINT,HOTPLUG,MODEL"],
                output: .string,
                error: .string
            )
            
            if result.terminationStatus.isSuccess {
                if let output = result.standardOutput, !output.isEmpty {
                    // Parse the output into Drive objects
                    return parseLsblkOutput(output, all: all)
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
            // Use lsblk to get information about a specific device
            // Normalize the id to remove /dev/ prefix if present
            let deviceId = id.hasPrefix("/dev/") ? String(id.dropFirst(5)) : id
            
            let result = try await Subprocess.run(
                Subprocess.Executable.name("lsblk"),
                arguments: ["-b", "-J", "-o", "NAME,SIZE,MOUNTPOINT,HOTPLUG,MODEL", "/dev/\(deviceId)"],
                output: .string,
                error: .string
            )
            
            if result.terminationStatus.isSuccess {
                if let output = result.standardOutput, !output.isEmpty {
                    // Parse the output to get drive information
                    let drives = parseLsblkOutput(output, all: true)
                    if let drive = drives.first {
                        return drive
                    }
                }
            }
            
            // If we couldn't find the drive with lsblk, try another approach
            // Use df to get available space information
            var available: Int64 = 0
            var capacity: Int64 = 0
            var name = "Unknown"
            var isExternal = false
            
            // Try to get device information using udevadm
            let udevResult = try await Subprocess.run(
                Subprocess.Executable.name("udevadm"),
                arguments: ["info", "--query=property", "--name=/dev/\(deviceId)"],
                output: .string,
                error: .string
            )
            
            if udevResult.terminationStatus.isSuccess, let udevOutput = udevResult.standardOutput {
                // Parse udevadm output
                let lines = udevOutput.split(separator: "\n")
                for line in lines {
                    if line.hasPrefix("ID_MODEL=") {
                        name = String(line.dropFirst(9)).replacingOccurrences(of: "\"", with: "")
                    } else if line.hasPrefix("ID_BUS=") {
                        // Check if it's external (usb, firewire, etc.)
                        let bus = String(line.dropFirst(7))
                        isExternal = (bus == "usb" || bus == "ieee1394")
                    }
                }
            }
            
            // Get size information from /sys/block
            let sizeResult = try? await Subprocess.run(
                Subprocess.Executable.name("cat"),
                arguments: ["/sys/block/\(deviceId)/size"],
                output: .string,
                error: .string
            )
            
            if let sizeResult = sizeResult, 
               sizeResult.terminationStatus.isSuccess, 
               let sizeOutput = sizeResult.standardOutput,
               let sizeBlocks = Int64(sizeOutput.trimmingCharacters(in: .whitespacesAndNewlines)) {
                // Convert 512-byte blocks to bytes
                capacity = sizeBlocks * 512
            }
            
            // Get available space using df
            let dfResult = try? await Subprocess.run(
                Subprocess.Executable.name("df"),
                arguments: ["-B1", "/dev/\(deviceId)"],
                output: .string,
                error: .string
            )
            
            if let dfResult = dfResult,
               dfResult.terminationStatus.isSuccess,
               let dfOutput = dfResult.standardOutput {
                let lines = dfOutput.split(separator: "\n")
                if lines.count > 1 {
                    let parts = lines[1].split(separator: " ").filter { !$0.isEmpty }
                    if parts.count >= 4, let availableBytes = Int64(parts[3]) {
                        available = availableBytes
                    }
                }
            }
            
            // If we have at least the capacity, return a Drive object
            if capacity > 0 {
                return Drive(
                    id: "/dev/\(deviceId)",
                    name: name,
                    available: available,
                    capacity: capacity,
                    isExternal: isExternal
                )
            }
            
            // If we couldn't find the drive, throw an error
            throw DiskListerError.driveNotFound(id: id)
        } catch {
            throw DiskListerError.driveNotFound(id: id)
        }
    }
    
    private func parseLsblkOutput(_ output: String, all: Bool) -> [Drive] {
        var drives: [Drive] = []
        
        // Try to parse the JSON output from lsblk -J
        guard let jsonData = output.data(using: .utf8) else {
            return []
        }
        
        do {
            // Parse the JSON output
            if let json = try JSONSerialization.jsonObject(with: jsonData) as? [String: Any],
               let blockdevices = json["blockdevices"] as? [[String: Any]] {
                
                for device in blockdevices {
                    // Skip loop devices and partitions
                    if let name = device["name"] as? String,
                       !name.starts(with: "loop") {
                        
                        let isExternal = (device["hotplug"] as? Int) == 1
                        
                        // Only include external devices unless all is true
                        if all || isExternal {
                            let id = "/dev/\(name)"
                            let model = device["model"] as? String ?? "Disk"
                            let displayName = model.isEmpty ? "Linux Disk" : model
                            
                            // Size is in bytes
                            let capacity = device["size"] as? Int64 ?? 0
                            
                            // Calculate available space (would need df command for accurate values)
                            // For now, we'll set it to 0 and could improve this in the future
                            let available: Int64 = 0
                            
                            let drive = Drive(
                                id: id,
                                name: displayName,
                                available: available,
                                capacity: capacity,
                                isExternal: isExternal
                            )
                            
                            drives.append(drive)
                        }
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
            let components = line.split(separator: " ").map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            
            // Basic parsing of lsblk text output
            if components.count >= 2 {
                let name = components[0]
                
                // Skip loop devices and partitions
                if !name.starts(with: "loop") && !name.contains("├") && !name.contains("└") {
                    let id = "/dev/\(name)"
                    let sizeStr = components[1]
                    
                    // Parse size (convert from human-readable to bytes)
                    var capacity: Int64 = 0
                    if sizeStr.hasSuffix("G") {
                        let sizeValue = Double(sizeStr.dropLast()) ?? 0
                        capacity = Int64(sizeValue * 1_000_000_000)
                    } else if sizeStr.hasSuffix("T") {
                        let sizeValue = Double(sizeStr.dropLast()) ?? 0
                        capacity = Int64(sizeValue * 1_000_000_000_000)
                    } else if sizeStr.hasSuffix("M") {
                        let sizeValue = Double(sizeStr.dropLast()) ?? 0
                        capacity = Int64(sizeValue * 1_000_000)
                    }
                    
                    // We don't have a reliable way to determine if a drive is external from text output
                    // Assume all drives are internal unless they have a specific pattern
                    let isExternal = name.starts(with: "sd") && !name.starts(with: "sda")
                    
                    // Only include external devices unless all is true
                    if all || isExternal {
                        let drive = Drive(
                            id: id,
                            name: "Linux Disk \(name)",
                            available: 0, // We don't have this information
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
