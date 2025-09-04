#if os(macOS)

    import Foundation
    import SystemConfiguration

    /// Protocol for abstracting SystemConfiguration operations to enable testing
    protocol SystemConfigurationServiceProtocol {
        /// Creates a SystemConfiguration dynamic store
        func createDynamicStore(
            name: CFString,
            callback: SCDynamicStoreCallBack?,
            context: UnsafeMutablePointer<SCDynamicStoreContext>?
        ) -> SCDynamicStore?

        /// Sets notification keys for the dynamic store
        func setNotificationKeys(
            store: SCDynamicStore,
            keys: CFArray?,
            patterns: CFArray?
        ) -> Bool

        /// Creates a run loop source for the dynamic store
        func createRunLoopSource(
            store: SCDynamicStore,
            order: CFIndex
        ) -> CFRunLoopSource?

        /// Copies a value from the dynamic store
        func copyValue(
            store: SCDynamicStore,
            key: CFString
        ) -> CFPropertyList?

        /// Adds a run loop source to the main run loop
        func addRunLoopSource(
            source: CFRunLoopSource,
            mode: CFRunLoopMode
        )

        /// Removes a run loop source from the main run loop
        func removeRunLoopSource(
            source: CFRunLoopSource,
            mode: CFRunLoopMode
        )
    }

    /// Real implementation that wraps actual SystemConfiguration APIs
    struct RealSystemConfigurationService: SystemConfigurationServiceProtocol {

        func createDynamicStore(
            name: CFString,
            callback: SCDynamicStoreCallBack?,
            context: UnsafeMutablePointer<SCDynamicStoreContext>?
        ) -> SCDynamicStore? {
            return SCDynamicStoreCreate(kCFAllocatorDefault, name, callback, context)
        }

        func setNotificationKeys(
            store: SCDynamicStore,
            keys: CFArray?,
            patterns: CFArray?
        ) -> Bool {
            return SCDynamicStoreSetNotificationKeys(store, keys, patterns)
        }

        func createRunLoopSource(
            store: SCDynamicStore,
            order: CFIndex
        ) -> CFRunLoopSource? {
            return SCDynamicStoreCreateRunLoopSource(kCFAllocatorDefault, store, order)
        }

        func copyValue(
            store: SCDynamicStore,
            key: CFString
        ) -> CFPropertyList? {
            return SCDynamicStoreCopyValue(store, key)
        }

        func addRunLoopSource(
            source: CFRunLoopSource,
            mode: CFRunLoopMode
        ) {
            CFRunLoopAddSource(CFRunLoopGetMain(), source, mode)
        }

        func removeRunLoopSource(
            source: CFRunLoopSource,
            mode: CFRunLoopMode
        ) {
            CFRunLoopRemoveSource(CFRunLoopGetMain(), source, mode)
        }
    }

#endif  // os(macOS)
