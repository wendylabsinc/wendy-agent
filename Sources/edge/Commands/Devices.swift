import ArgumentParser
import IOKit
import IOKit.usb
import Network
import Foundation
import SystemConfiguration
import Logging
struct DevicesCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "devices",
        abstract: "List USB and Ethernet devices connected to the system"
    )
    
    // @Flag(name: .long, help: "List Ethernet interfaces")
    // var showEthernet = false
    
    // @Flag(name: .long, help: "List USB devices")
    // var showUSB = true

    func run() async throws {
        let logger = Logger(label: "apache-edge.cli.run")
        // If no specific flag is set, show both
        // let showBoth = !showEthernet
        listUSBDevices(logger: logger)
        listEthernetInterfaces()
        // if showUSB || showBoth {
        //     // USB Devices
        //     listUSBDevices()
        // }
        
        // if showEthernet || showBoth {
        //     // List Ethernet interfaces
        //     listEthernetInterfaces()
        // }
    }
    
    func listUSBDevices(logger: Logger) {
        let matchingDict = IOServiceMatching(kIOUSBDeviceClassName)
        var iterator: io_iterator_t = 0
    
        let result = IOServiceGetMatchingServices(kIOMainPortDefault, matchingDict, &iterator)
        if result != KERN_SUCCESS {
            logger.error("Error: \(result)")
            return
        }

        var usbDevice = IOIteratorNext(iterator)
        var foundEdgeOSDevices = false
        
        logger.info("USB Devices:")
        while usbDevice != 0 {
            logger.debug("usbDevice: \(usbDevice)")
            // Get device properties
            let deviceRef = IORegistryEntryCreateCFProperty(usbDevice, "USB Product Name" as CFString, kCFAllocatorDefault, 0)
            if let deviceName = deviceRef?.takeRetainedValue() as? String {
                logger.debug("deviceName: \(deviceName)")
                // Only display devices that include "EdgeOS" in their name
                if !deviceName.contains("EdgeOS") {
                    continue
                }
                foundEdgeOSDevices = true
                logger.info("USB Device: \(deviceName)")
                
                // Get vendor ID and product ID
                let vendorIdRef = IORegistryEntryCreateCFProperty(usbDevice, "idVendor" as CFString, kCFAllocatorDefault, 0)
                let productIdRef = IORegistryEntryCreateCFProperty(usbDevice, "idProduct" as CFString, kCFAllocatorDefault, 0)
                
                if let vendorId = vendorIdRef?.takeRetainedValue() as? Int,
                    let productId = productIdRef?.takeRetainedValue() as? Int {
                    logger.info("  Vendor ID: \(String(format: "0x%04X", vendorId)), Product ID: \(String(format: "0x%04X", productId))")
                }
            }
            
            IOObjectRelease(usbDevice)
            usbDevice = IOIteratorNext(iterator)
        }
        
        if !foundEdgeOSDevices {
            logger.info("No EdgeOS devices found.")
        }
        
        IOObjectRelease(iterator)
    }

    func listEthernetInterfaces() {
        guard let interfaces = SCNetworkInterfaceCopyAll() as? [SCNetworkInterface] else {
            print("Failed to get network interfaces")
            return
        }
        
        var foundEdgeOSInterfaces = false
        print("\nEthernet Interfaces:")
        
        for interface in interfaces {
            // Check if it's an Ethernet interface
            if let interfaceType = SCNetworkInterfaceGetInterfaceType(interface) as? String,
               (interfaceType == kSCNetworkInterfaceTypeEthernet as String || 
                interfaceType == kSCNetworkInterfaceTypeIEEE80211 as String ||  // WiFi
                interfaceType == kSCNetworkInterfaceTypePPP as String || 
                interfaceType == kSCNetworkInterfaceTypeBond as String) {
                
                // Get interface details
                let name = SCNetworkInterfaceGetBSDName(interface) as? String ?? "Unknown"
                let displayName = SCNetworkInterfaceGetLocalizedDisplayName(interface) as? String ?? "Unknown"
                
                // Only show interfaces containing "EdgeOS" in their name
                if !displayName.contains("EdgeOS") && !name.contains("EdgeOS") {
                    continue
                }
                
                foundEdgeOSInterfaces = true
                print("- \(displayName) (\(name)) [\(interfaceType)]")
                
                // Get MAC address for physical interfaces
                if interfaceType == kSCNetworkInterfaceTypeEthernet as String || interfaceType == kSCNetworkInterfaceTypeIEEE80211 as String {
                    if let macAddr = SCNetworkInterfaceGetHardwareAddressString(interface) as? String {
                        print("  MAC Address: \(macAddr)")
                    }
                }
            }
        }
        
        if !foundEdgeOSInterfaces {
            print("No EdgeOS Ethernet interfaces found.")
        }
    }

    func listNetworkInterfaces() {
        let monitor = NWPathMonitor()
        monitor.pathUpdateHandler = { path in
            if path.status == .satisfied {
                if path.usesInterfaceType(.wifi) {
                    print("Connected via Wi-Fi")
                } else if path.usesInterfaceType(.wiredEthernet) {
                    print("Connected via Ethernet")
                } else if path.usesInterfaceType(.cellular) {
                    print("Connected via Cellular")
                }
                
                // List all available interfaces
                for interface in path.availableInterfaces {
                    print("Interface: \(interface.name)")
                }
            } else {
                print("No connection")
            }
        }
        
        // Start monitoring on a background queue
        let queue = DispatchQueue(label: "NetworkMonitor")
        monitor.start(queue: queue)
        
        // Keep the monitor running (in a real app, you'd manage this differently)
        RunLoop.main.run(until: Date(timeIntervalSinceNow: 5))
    }
}