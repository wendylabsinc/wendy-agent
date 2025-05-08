import Logging

protocol DeviceDiscovery {
    func findUSBDevices(logger: Logger) async -> [USBDevice]
    func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface]
    func findLANDevices(logger: Logger) async throws -> [LANDevice]
}
