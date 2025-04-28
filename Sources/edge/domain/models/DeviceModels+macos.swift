#if os(macOS)
    import Foundation
    import Logging
    import IOKit
    import IOKit.usb
    import Network
    import SystemConfiguration

    // macOS specific extension for USBDevice
    extension USBDevice {
        static func fromIORegistryEntry(_ device: io_service_t) -> USBDevice? {
            guard
                let nameRef = IORegistryEntryCreateCFProperty(
                    device,
                    "USB Product Name" as CFString,
                    kCFAllocatorDefault,
                    0
                ),
                let deviceName = nameRef.takeRetainedValue() as? String,
                let vendorIdRef = IORegistryEntryCreateCFProperty(
                    device,
                    "idVendor" as CFString,
                    kCFAllocatorDefault,
                    0
                ),
                let vendorId = vendorIdRef.takeRetainedValue() as? Int,
                let productIdRef = IORegistryEntryCreateCFProperty(
                    device,
                    "idProduct" as CFString,
                    kCFAllocatorDefault,
                    0
                ),
                let productId = productIdRef.takeRetainedValue() as? Int
            else {
                return nil
            }

            return USBDevice(name: deviceName, vendorId: vendorId, productId: productId)
        }
    }
#endif
