#if !os(macOS) && !os(Linux)
import Foundation
import Logging

struct PlatformDeviceDiscovery: DeviceDiscovery {
    func listUSBDevices(logger: Logger) {
        print("Device listing is not supported on this platform")
        logger.warning("Device listing is not supported on this platform")
    }
    
    func listEthernetInterfaces(logger: Logger) {
        print("Interface listing is not supported on this platform")
        logger.warning("Interface listing is not supported on this platform")
    }
}
#endif 