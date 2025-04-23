#if os(Linux)
    import Foundation
    import Logging
    import Shell

    // Models for the JSON response from ip -j link
    struct NetworkInterface: Decodable {
        let ifindex: Int
        let ifname: String
        let flags: [String]
        let mtu: Int
        let operstate: String
        let linkmode: String
        let address: String
        let broadcast: String?
        let linkinfo: LinkInfo?
        
        struct LinkInfo: Decodable {
            let infoKind: String?
            
            enum CodingKeys: String, CodingKey {
                case infoKind = "info_kind"
            }
        }
    }

    struct PlatformDeviceDiscovery: DeviceDiscovery {
        func listUSBDevices(logger: Logger) async {
            logger.info("Listing USB devices on Linux")

            var foundEdgeOSDevices = false
            
            do {
                let output = try await Shell.run(["lsusb"])
                print("USB Devices:")
                
                for line in output.split(separator: "\n") {
                    let deviceInfo = String(line)
                    logger.debug("Found USB device: \(deviceInfo)")
                    
                    // Filter for EdgeOS devices
                    if deviceInfo.contains("EdgeOS") {
                        print(deviceInfo)
                        foundEdgeOSDevices = true
                    }
                }
            } catch {
                logger.error("Failed to list USB devices: \(error)")
            }

            if !foundEdgeOSDevices {
                print("No EdgeOS USB devices found.")
            }
        }

        func listEthernetInterfaces(logger: Logger) async {
            logger.info("Listing Ethernet interfaces on Linux")

            var foundEdgeOSInterfaces = false
            
            do {
                // Use JSON output for more reliable parsing
                let rawOutput = try await Shell.run(["ip", "-j", "link", "show"])
                print("\nEthernet Interfaces:")
                
                // Clean up the output - remove any error messages or stack traces
                let jsonOutput = sanitizeJsonOutput(rawOutput)
                
                // Parse JSON data with JSONDecoder
                do {
                    let jsonData = jsonOutput.data(using: .utf8) ?? Data()
                    let decoder = JSONDecoder()
                    let interfaces = try decoder.decode([NetworkInterface].self, from: jsonData)
                    
                    processInterfaces(interfaces: interfaces, logger: logger, foundEdgeOSInterfaces: &foundEdgeOSInterfaces)
                    
                } catch {
                    logger.warning("JSON decoding error: \(error)")
                    logger.warning("Raw output: \(jsonOutput)")
                    fallbackInterfaceParsing(output: rawOutput, logger: logger, foundEdgeOSInterfaces: &foundEdgeOSInterfaces)
                }
            } catch {
                logger.error("Failed to list Ethernet interfaces: \(error)")
            }

            if !foundEdgeOSInterfaces {
                print("No EdgeOS Ethernet interfaces found.")
            }
        }
        
        // Process the parsed JSON interfaces
        private func processInterfaces(interfaces: [NetworkInterface], logger: Logger, foundEdgeOSInterfaces: inout Bool) {
            for interface in interfaces {
                let ifaceName = interface.ifname
                
                // Skip loopback and virtual interfaces
                if ifaceName == "lo" || ifaceName.hasPrefix("veth") || ifaceName.hasPrefix("docker") {
                    continue
                }
                
                let address = interface.address
                
                // Get driver info if available
                var driverInfo = ""
                if let linkInfo = interface.linkinfo, let infoKind = linkInfo.infoKind {
                    driverInfo = "Type: \(infoKind)"
                }
                
                // Get operational state
                let operState = interface.operstate
                
                // Create a display string with all the information
                let displayInfo = "\(ifaceName) (MAC: \(address)) \(driverInfo)"
                
                // Filter for EdgeOS interfaces
                if displayInfo.contains("EdgeOS") || ifaceName.contains("EdgeOS") {
                    // Print the interface details
                    print("- \(ifaceName)")
                    print("  MAC Address: \(address)")
                    print("  State: \(operState)")
                    if !driverInfo.isEmpty {
                        print("  \(driverInfo)")
                    }
                    
                    foundEdgeOSInterfaces = true
                }
            }
        }
        
        // Attempts to sanitize output to get valid JSON
        private func sanitizeJsonOutput(_ output: String) -> String {
            // Find the first '[' character - beginning of JSON array
            if let startIndex = output.firstIndex(of: "[") {
                let jsonSubstring = output[startIndex...]
                
                // Now find a proper JSON ending - last ']' character
                if let endIndex = jsonSubstring.lastIndex(of: "]"),
                   endIndex > startIndex {
                    return String(jsonSubstring[...endIndex])
                }
            }
            return output // Return original if no proper JSON found
        }
        
        private func fallbackInterfaceParsing(output: String, logger: Logger, foundEdgeOSInterfaces: inout Bool) {
            logger.info("Using fallback parsing for interface information")
            
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
    }
#endif
