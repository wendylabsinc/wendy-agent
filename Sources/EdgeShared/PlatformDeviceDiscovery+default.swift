#if !os(macOS) && !os(Linux)
    import Foundation
    import Logging

    public struct PlatformDeviceDiscovery: DeviceDiscovery {
        public init() {}

        public func findUSBDevices(logger: Logger) async -> [USBDevice] {
            logger.warning("Device listing is not supported on this platform")
            return []
        }

        public func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface] {
            logger.warning("Interface listing is not supported on this platform")
            return []
        }

        public func findLANDevices(logger: Logger) async throws -> [LANDevice] {
            logger.warning("LAN device listing is not supported on this platform")
            return []
        }
    }
#endif
