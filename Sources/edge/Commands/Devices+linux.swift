#if os(Linux)
import Foundation
import Logging

struct PlatformDeviceDiscovery: DeviceDiscovery {
    func listUSBDevices(logger: Logger) {
        logger.info("Listing USB devices on Linux")
        
        var foundEdgeOSDevices = false
        
        // Use /sys/bus/usb/devices to list USB devices
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/bin/bash")
        task.arguments = ["-c", "lsusb"]
        
        let pipe = Pipe()
        task.standardOutput = pipe
        
        do {
            try task.run()
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            if let output = String(data: data, encoding: .utf8) {
                for line in output.split(separator: "\n") {
                    let deviceInfo = String(line)
                    logger.debug("Found USB device: \(deviceInfo)")
                    
                    // Filter for EdgeOS devices
                    if deviceInfo.contains("EdgeOS") {
                        print(deviceInfo)
                        foundEdgeOSDevices = true
                    }
                }
            }
            task.waitUntilExit()
        } catch {
            logger.error("Failed to list USB devices: \(error)")
        }
        
        if !foundEdgeOSDevices {
            logger.info("No EdgeOS USB devices found")
        }
    }
    
    func listEthernetInterfaces(logger: Logger) {
        logger.info("Listing Ethernet interfaces on Linux")
        
        var foundEdgeOSInterfaces = false
        
        // Use ip link show to list network interfaces
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/bin/bash")
        task.arguments = ["-c", "ip link show"]
        
        let pipe = Pipe()
        task.standardOutput = pipe
        
        do {
            try task.run()
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            if let output = String(data: data, encoding: .utf8) {
                print("\nEthernet Interfaces:")
                
                var currentInterface = ""
                for line in output.split(separator: "\n") {
                    let interfaceInfo = String(line)
                    
                    // New interface entry typically starts without a space
                    if !interfaceInfo.hasPrefix(" ") {
                        // Check if this is a network interface line
                        if interfaceInfo.contains(": ") {
                            currentInterface = interfaceInfo
                            
                            // Filter for EdgeOS interfaces
                            if currentInterface.contains("EdgeOS") {
                                print(currentInterface)
                                foundEdgeOSInterfaces = true
                            }
                        }
                    } else if foundEdgeOSInterfaces && currentInterface.contains("EdgeOS") {
                        // This is a continuation of the current interface output
                        print(interfaceInfo)
                    }
                }
            }
            task.waitUntilExit()
        } catch {
            logger.error("Failed to list Ethernet interfaces: \(error)")
        }
        
        if !foundEdgeOSInterfaces {
            print("No EdgeOS Ethernet interfaces found.")
        }
    }
}
#endif
