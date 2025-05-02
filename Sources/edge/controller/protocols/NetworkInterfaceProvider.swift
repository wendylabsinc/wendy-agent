#if os(macOS)
    import Foundation
    import SystemConfiguration

    /// Protocol that abstracts SystemConfiguration network interface operations to allow for dependency injection and testing
    protocol NetworkInterfaceProvider {
        /// Gets all network interfaces
        func copyAllNetworkInterfaces() -> [SCNetworkInterface]?

        /// Gets the interface type of a network interface
        func getInterfaceType(interface: SCNetworkInterface) -> String?

        /// Gets the BSD name of a network interface
        func getBSDName(interface: SCNetworkInterface) -> String?

        /// Gets the localized display name of a network interface
        func getLocalizedDisplayName(interface: SCNetworkInterface) -> String?

        /// Gets the hardware address (MAC) of a network interface
        func getHardwareAddressString(interface: SCNetworkInterface) -> String?
    }

    /// Default implementation that uses the real SystemConfiguration APIs
    class DefaultNetworkInterfaceProvider: NetworkInterfaceProvider {
        func copyAllNetworkInterfaces() -> [SCNetworkInterface]? {
            return SCNetworkInterfaceCopyAll() as? [SCNetworkInterface]
        }

        func getInterfaceType(interface: SCNetworkInterface) -> String? {
            return SCNetworkInterfaceGetInterfaceType(interface) as? String
        }

        func getBSDName(interface: SCNetworkInterface) -> String? {
            return SCNetworkInterfaceGetBSDName(interface) as? String
        }

        func getLocalizedDisplayName(interface: SCNetworkInterface) -> String? {
            return SCNetworkInterfaceGetLocalizedDisplayName(interface) as? String
        }

        func getHardwareAddressString(interface: SCNetworkInterface) -> String? {
            return SCNetworkInterfaceGetHardwareAddressString(interface) as? String
        }
    }
#endif
