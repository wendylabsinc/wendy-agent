#if os(macOS)

    import WendyShared
    import Foundation
    import Logging
    import SystemConfiguration

    /// Protocol for network interface monitoring
    protocol NetworkInterfaceMonitorProtocol: Actor {
        func start() async throws
        func stop() async
        func setInterfaceHandler(
            _ handler: @escaping @Sendable (NetworkInterfaceEvent) -> Void
        ) async
    }

    /// Event-driven network interface monitor using SystemConfiguration framework
    /// This monitors for network interface availability changes in real-time
    actor NetworkInterfaceMonitor: NetworkInterfaceMonitorProtocol {
        private let logger: Logger
        private var dynamicStore: SCDynamicStore?
        private var runLoopSource: CFRunLoopSource?
        private var isRunning = false
        private var interfaceHandler: (@Sendable (NetworkInterfaceEvent) -> Void)?
        private var monitoringTask: Task<Void, Never>?
        private var knownInterfaces: Set<String> = []

        // Dependency injection
        private let systemConfigService: SystemConfigurationServiceProtocol

        init(
            logger: Logger,
            systemConfigService: SystemConfigurationServiceProtocol? = nil
        ) {
            self.logger = logger
            self.systemConfigService = systemConfigService ?? RealSystemConfigurationService()
        }

        func start() async throws {
            guard !isRunning else {
                logger.debug("Network interface monitor already running")
                return
            }

            logger.info("Starting network interface monitoring...")

            // Create dynamic store session
            var context = SCDynamicStoreContext(
                version: 0,
                info: Unmanaged.passUnretained(self).toOpaque(),
                retain: nil,
                release: nil,
                copyDescription: nil
            )

            let callback: SCDynamicStoreCallBack = { (store, changedKeys, info) in
                guard let info = info else { return }
                let monitor = Unmanaged<NetworkInterfaceMonitor>.fromOpaque(info)
                    .takeUnretainedValue()

                // Copy the keys array to avoid data races
                let keysCopy = (changedKeys as? [String]) ?? []

                Task { @MainActor in
                    await monitor.handleNetworkChange(changedKeys: keysCopy)
                }
            }

            dynamicStore = systemConfigService.createDynamicStore(
                name: "sh.wendy.wendy-helper.network-monitor" as CFString,
                callback: callback,
                context: &context
            )

            guard let dynamicStore = dynamicStore else {
                throw NetworkInterfaceMonitorError.failedToCreateDynamicStore
            }

            // Set up notification keys - monitor all network interfaces
            let notificationKeys =
                [
                    "State:/Network/Interface" as CFString
                ] as CFArray

            let notificationPatterns =
                [
                    "State:/Network/Interface/.*/Link" as CFString,
                    "State:/Network/Interface/.*/IPv4" as CFString,
                ] as CFArray

            let setKeysResult = systemConfigService.setNotificationKeys(
                store: dynamicStore,
                keys: notificationKeys,
                patterns: notificationPatterns
            )

            guard setKeysResult else {
                throw NetworkInterfaceMonitorError.failedToSetNotificationKeys
            }

            // Create run loop source
            runLoopSource = systemConfigService.createRunLoopSource(
                store: dynamicStore,
                order: 0
            )

            guard runLoopSource != nil else {
                throw NetworkInterfaceMonitorError.failedToCreateRunLoopSource
            }

            // Get initial state of interfaces
            await loadInitialInterfaceState()

            // Start the run loop monitoring task
            monitoringTask = Task {
                await withTaskCancellationHandler {
                    await runNotificationLoop()
                } onCancel: {
                    // Cleanup will be handled in stop()
                }
            }

            isRunning = true
            logger.info("✅ Network interface monitoring started")
        }

        func stop() async {
            guard isRunning else { return }

            logger.info("Stopping network interface monitoring...")

            // Cancel monitoring task
            monitoringTask?.cancel()
            monitoringTask = nil

            // Clean up SystemConfiguration resources
            if let runLoopSource = runLoopSource {
                systemConfigService.removeRunLoopSource(
                    source: runLoopSource,
                    mode: CFRunLoopMode.defaultMode
                )
                self.runLoopSource = nil
            }

            if let dynamicStore = dynamicStore {
                _ = systemConfigService.setNotificationKeys(
                    store: dynamicStore,
                    keys: nil,
                    patterns: nil
                )
                self.dynamicStore = nil
            }

            knownInterfaces.removeAll()
            isRunning = false

            logger.info("✅ Network interface monitoring stopped")
        }

        func setInterfaceHandler(
            _ handler: @escaping @Sendable (NetworkInterfaceEvent) -> Void
        ) async {
            self.interfaceHandler = handler
            logger.debug("Network interface handler set")
        }

        // MARK: - Private Methods

        private func runNotificationLoop() async {
            logger.debug("Starting network interface notification run loop...")

            // Add the notification source to the main run loop
            if let source = runLoopSource {
                systemConfigService.addRunLoopSource(
                    source: source,
                    mode: CFRunLoopMode.defaultMode
                )
                logger.debug("Added network interface notification source to main run loop")
            }

            // Keep the task alive to maintain the run loop source
            while !Task.isCancelled && isRunning {
                try? await Task.sleep(for: .seconds(1))
            }

            logger.debug("Network interface notification run loop ended")
        }

        private func loadInitialInterfaceState() async {
            guard let dynamicStore = dynamicStore else { return }

            // Get list of current network interfaces
            let interfaceListKey = "State:/Network/Interface" as CFString
            if let interfaceList = systemConfigService.copyValue(
                store: dynamicStore,
                key: interfaceListKey
            ) as? [String] {
                knownInterfaces = Set(interfaceList)
                logger.debug("Initial network interfaces: \(knownInterfaces)")
            }
        }

        private func handleNetworkChange(changedKeys: [String]) async {
            logger.debug("Network change detected for keys: \(changedKeys)")

            guard let dynamicStore = dynamicStore else { return }

            // Check for interface additions/removals
            await checkForInterfaceChanges()

            // Check for Wendy interface events specifically
            for key in changedKeys {
                if key.contains("/Link") {
                    await handleLinkStatusChange(key: key, dynamicStore: dynamicStore)
                } else if key.contains("/IPv4") {
                    await handleIPv4Change(key: key, dynamicStore: dynamicStore)
                }
            }
        }

        private func checkForInterfaceChanges() async {
            guard let dynamicStore = dynamicStore else { return }

            // Get current interface list
            let interfaceListKey = "State:/Network/Interface" as CFString
            guard
                let currentInterfaces = systemConfigService.copyValue(
                    store: dynamicStore,
                    key: interfaceListKey
                ) as? [String]
            else {
                return
            }

            let currentSet = Set(currentInterfaces)

            // Find new interfaces
            let newInterfaces = currentSet.subtracting(knownInterfaces)
            for interface in newInterfaces {
                await checkIfWendyInterface(interface: interface, isAppearing: true)
            }

            // Find removed interfaces
            let removedInterfaces = knownInterfaces.subtracting(currentSet)
            for interface in removedInterfaces {
                await notifyInterfaceRemoved(interface: interface)
            }

            knownInterfaces = currentSet
        }

        private func handleLinkStatusChange(key: String, dynamicStore: SCDynamicStore) async {
            // Extract interface name from key like "State:/Network/Interface/en5/Link"
            let components = key.components(separatedBy: "/")
            guard components.count >= 4 else { return }

            let interfaceName = components[3]

            // Check if this is an Wendy interface and if link is up
            if let linkInfo = systemConfigService.copyValue(
                store: dynamicStore,
                key: key as CFString
            ) as? [String: Any],
                let linkStatus = linkInfo["Active"] as? Bool,
                linkStatus
            {
                await checkIfWendyInterface(interface: interfaceName, isAppearing: true)
            }
        }

        private func handleIPv4Change(key: String, dynamicStore: SCDynamicStore) async {
            // Extract interface name from key like "State:/Network/Interface/en5/IPv4"
            let components = key.components(separatedBy: "/")
            guard components.count >= 4 else { return }

            let interfaceName = components[3]

            // Check if IPv4 configuration appeared (interface became ready)
            if systemConfigService.copyValue(
                store: dynamicStore,
                key: key as CFString
            ) != nil {
                await checkIfWendyInterface(interface: interfaceName, isAppearing: true)
            }
        }

        private func checkIfWendyInterface(interface: String, isAppearing: Bool) async {
            // For interface appearance events, we check if it's an Ethernet interface that could be Wendy
            // We can't rely on the interface name containing "Wendy" because the system assigns
            // generic names like en5, en6, etc. Instead, we trigger for any Ethernet interface
            // and let the daemon correlate it with pending USB devices.

            if interface.hasPrefix("en") || interface.hasPrefix("eth") {
                let event: NetworkInterfaceEvent =
                    isAppearing ? .interfaceAppeared(interface) : .interfaceDisappeared(interface)

                logger.info(
                    "Ethernet interface \(isAppearing ? "appeared" : "disappeared"): \(interface)"
                )
                interfaceHandler?(event)
            }
        }

        private func notifyInterfaceRemoved(interface: String) async {
            let event: NetworkInterfaceEvent = .interfaceDisappeared(interface)
            logger.info("Network interface disappeared: \(interface)")
            interfaceHandler?(event)
        }
    }

    // MARK: - Event Types

    enum NetworkInterfaceEvent {
        case interfaceAppeared(String)  // Interface name (e.g., "en5")
        case interfaceDisappeared(String)  // Interface name (e.g., "en5")
    }

    // MARK: - Error Types

    enum NetworkInterfaceMonitorError: Error, LocalizedError {
        case failedToCreateDynamicStore
        case failedToSetNotificationKeys
        case failedToCreateRunLoopSource

        var errorDescription: String? {
            switch self {
            case .failedToCreateDynamicStore:
                return "Failed to create SystemConfiguration dynamic store"
            case .failedToSetNotificationKeys:
                return "Failed to set network interface notification keys"
            case .failedToCreateRunLoopSource:
                return "Failed to create run loop source for network monitoring"
            }
        }
    }

#endif  // os(macOS)
