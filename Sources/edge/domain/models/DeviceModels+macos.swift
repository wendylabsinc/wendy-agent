#if os(macOS)
    import Foundation
    import Logging
    import IOKit
    import IOKit.usb
    import Network
    import SystemConfiguration

    // macOS specific extension for USBDevice
    extension USBDevice {
        static func fromIORegistryEntry(
            _ device: io_service_t,
            provider: IOServiceProvider? = nil
        ) -> USBDevice? {
            let ioProvider = provider ?? DefaultIOServiceProvider()

            // Get device properties using the provided IOServiceProvider
            guard
                let deviceName = ioProvider.getRegistryEntryProperty(
                    device: device,
                    key: "USB Product Name" as CFString
                ) as? String,
                let vendorId = ioProvider.getRegistryEntryProperty(
                    device: device,
                    key: "idVendor" as CFString
                ) as? Int,
                let productId = ioProvider.getRegistryEntryProperty(
                    device: device,
                    key: "idProduct" as CFString
                ) as? Int
            else {
                return nil
            }

            return USBDevice(name: deviceName, vendorId: vendorId, productId: productId)
        }
    }
#endif
