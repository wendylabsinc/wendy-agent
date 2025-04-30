#if os(macOS)
    import Foundation
    import IOKit

    /// Protocol that abstracts IOKit service operations to allow for dependency injection and testing
    protocol IOServiceProvider {
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
    class DefaultIOServiceProvider: IOServiceProvider {
        func createMatchingDictionary(className: String) -> CFDictionary? {
            return IOServiceMatching(className)
        }

        func getMatchingServices(
            masterPort: mach_port_t,
            matchingDict: CFDictionary?,
            iterator: UnsafeMutablePointer<io_iterator_t>
        ) -> kern_return_t {
            return IOServiceGetMatchingServices(masterPort, matchingDict, iterator)
        }

        func getNextItem(iterator: io_iterator_t) -> io_service_t {
            return IOIteratorNext(iterator)
        }

        func releaseIOObject(object: io_service_t) {
            IOObjectRelease(object)
        }

        func getRegistryEntryProperty(device: io_service_t, key: CFString) -> Any? {
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
