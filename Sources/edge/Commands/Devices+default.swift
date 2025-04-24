#if !os(macOS) && !os(Linux)
    import Foundation
    import Logging

    struct PlatformDeviceDiscovery: DeviceDiscovery {
        func findUSBDevices(logger: Logger) async -> [USBDevice] {
            logger.warning("Device listing is not supported on this platform")
            return []
        }

        func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface] {
            logger.warning("Interface listing is not supported on this platform")
            return []
        }
    }
#endif
