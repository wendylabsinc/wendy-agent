#if os(macOS)

    import Foundation
    import IOKit
    import IOKit.usb
    import IOKit.usb.IOUSBLib

    /// Protocol for abstracting IOKit operations to enable testing
    protocol IOKitServiceProtocol {
        /// Creates an IOKit notification port
        func createNotificationPort() -> IONotificationPortRef?

        /// Gets the run loop source for a notification port
        func getRunLoopSource(_ port: IONotificationPortRef) -> CFRunLoopSource?

        /// Creates a matching dictionary for USB devices
        func createUSBDeviceMatchingDictionary() -> CFMutableDictionary?

        /// Adds a matching notification for device events
        func addMatchingNotification(
            port: IONotificationPortRef,
            type: String,
            matching: CFMutableDictionary,
            callback: IOServiceMatchingCallback,
            refcon: UnsafeMutableRawPointer,
            iterator: UnsafeMutablePointer<io_iterator_t>
        ) -> kern_return_t

        /// Gets the next device from an iterator
        func iteratorNext(_ iterator: io_iterator_t) -> io_service_t

        /// Releases an IOKit object
        func objectRelease(_ object: io_object_t)

        /// Gets device properties from an io_service_t
        func getDeviceProperties(_ device: io_service_t) -> [String: Any]?

        /// Destroys a notification port
        func destroyNotificationPort(_ port: IONotificationPortRef)
    }

    /// Real implementation that wraps actual IOKit APIs
    struct RealIOKitService: IOKitServiceProtocol {

        func createNotificationPort() -> IONotificationPortRef? {
            return IONotificationPortCreate(kIOMainPortDefault)
        }

        func getRunLoopSource(_ port: IONotificationPortRef) -> CFRunLoopSource? {
            guard let unmanagedSource = IONotificationPortGetRunLoopSource(port) else {
                return nil
            }
            return unmanagedSource.takeUnretainedValue()
        }

        func createUSBDeviceMatchingDictionary() -> CFMutableDictionary? {
            return IOServiceMatching(kIOUSBDeviceClassName)
        }

        func addMatchingNotification(
            port: IONotificationPortRef,
            type: String,
            matching: CFMutableDictionary,
            callback: IOServiceMatchingCallback,
            refcon: UnsafeMutableRawPointer,
            iterator: UnsafeMutablePointer<io_iterator_t>
        ) -> kern_return_t {
            return IOServiceAddMatchingNotification(
                port,
                type,
                matching,
                callback,
                refcon,
                iterator
            )
        }

        func iteratorNext(_ iterator: io_iterator_t) -> io_service_t {
            return IOIteratorNext(iterator)
        }

        func objectRelease(_ object: io_object_t) {
            IOObjectRelease(object)
        }

        func getDeviceProperties(_ device: io_service_t) -> [String: Any]? {
            var properties: Unmanaged<CFMutableDictionary>?
            let result = IORegistryEntryCreateCFProperties(
                device,
                &properties,
                kCFAllocatorDefault,
                0
            )

            guard result == KERN_SUCCESS, let props = properties?.takeRetainedValue() else {
                return nil
            }

            return props as NSDictionary as? [String: Any]
        }

        func destroyNotificationPort(_ port: IONotificationPortRef) {
            IONotificationPortDestroy(port)
        }
    }

#endif  // os(macOS)
