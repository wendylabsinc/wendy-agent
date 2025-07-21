#if os(macOS)
    import Foundation
    import IOKit

    /// Protocol that abstracts IOKit service operations to allow for dependency injection and testing
    public protocol IOServiceProvider: Sendable {
        /// Creates a matching dictionary for service lookup
        func createMatchingDictionary(className: String) -> CFDictionary?

        /// Gets matching services and returns an iterator
        func getMatchingServices(
            masterPort: mach_port_t,
            matchingDict: CFDictionary?,
            iterator: UnsafeMutablePointer<io_iterator_t>
        ) -> kern_return_t

        /// Gets the next item from an iterator
        func getNextItem(iterator: io_iterator_t) -> io_service_t

        /// Releases an IO object
        func releaseIOObject(object: io_service_t)

        /// Gets a property from an IO registry entry
        func getRegistryEntryProperty(device: io_service_t, key: CFString) -> Any?
    }

    /// Default implementation that uses the real IOKit APIs
    public final class DefaultIOServiceProvider: IOServiceProvider {
        public init() {}

        public func createMatchingDictionary(className: String) -> CFDictionary? {
            return IOServiceMatching(className)
        }

        public func getMatchingServices(
            masterPort: mach_port_t,
            matchingDict: CFDictionary?,
            iterator: UnsafeMutablePointer<io_iterator_t>
        ) -> kern_return_t {
            return IOServiceGetMatchingServices(masterPort, matchingDict, iterator)
        }

        public func getNextItem(iterator: io_iterator_t) -> io_service_t {
            return IOIteratorNext(iterator)
        }

        public func releaseIOObject(object: io_service_t) {
            IOObjectRelease(object)
        }

        public func getRegistryEntryProperty(device: io_service_t, key: CFString) -> Any? {
            guard
                let propertyRef = IORegistryEntryCreateCFProperty(
                    device,
                    key,
                    kCFAllocatorDefault,
                    0
                )
            else {
                return nil
            }

            return propertyRef.takeRetainedValue()
        }
    }
#endif
