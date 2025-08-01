#if os(macOS)

    import EdgeShared
    import Foundation
    import Logging
    import IOKit
    import IOKit.usb
    import IOKit.usb.IOUSBLib

    /// Event-driven USB monitor using IOKit notifications
    /// This replaces polling with real-time USB device connection/disconnection events
    actor IOKitUSBMonitor: USBMonitorService {
        private let logger: Logger
        private var deviceHandler: (@Sendable (USBDeviceEvent) -> Void)?
        private var isRunning = false

        // IOKit notification handling
        private var notificationPort: IONotificationPortRef?
        private var runLoopSource: CFRunLoopSource?
        private var matchedIterator: io_iterator_t = 0
        private var terminatedIterator: io_iterator_t = 0
        private var runLoop: CFRunLoop?
        private var monitoringTask: Task<Void, Never>?

        // Dependency injection
        private let deviceInfoExtractor: USBDeviceInfoExtractorProtocol
        private let ioKitService: IOKitServiceProtocol

        init(
            logger: Logger,
            deviceInfoExtractor: USBDeviceInfoExtractorProtocol? = nil,
            ioKitService: IOKitServiceProtocol? = nil
        ) {
            self.logger = logger
            self.deviceInfoExtractor = deviceInfoExtractor ?? USBDeviceInfoExtractor(logger: logger)
            self.ioKitService = ioKitService ?? RealIOKitService()
        }

        func start() async throws {
            guard !isRunning else {
                logger.debug("IOKit USB monitor already running")
                return
            }

            logger.info("Starting IOKit USB device monitoring...")

            // Create notification port
            guard let notificationPort = ioKitService.createNotificationPort() else {
                throw USBMonitorError.failedToCreateNotificationPort
            }
            self.notificationPort = notificationPort

            // Get the run loop source
            guard let source = ioKitService.getRunLoopSource(notificationPort) else {
                throw USBMonitorError.failedToCreateNotificationPort
            }
            self.runLoopSource = source

            // Create matching dictionary for USB devices
            guard let matchingDict = ioKitService.createUSBDeviceMatchingDictionary() else {
                throw USBMonitorError.failedToCreateMatchingDictionary
            }

            // Add the notification port to a background run loop
            monitoringTask = Task {
                await withTaskCancellationHandler {
                    await runNotificationLoop()
                } onCancel: {
                    // Cleanup will be handled in stop()
                }
            }

            // Register for device matched notifications (device connected)
            let matchedCallback: IOServiceMatchingCallback = { (refcon, iterator) in
                let monitor = Unmanaged<IOKitUSBMonitor>.fromOpaque(refcon!).takeUnretainedValue()
                Task {
                    await monitor.handleDeviceMatched(iterator: iterator)
                }
            }

            let selfPtr = Unmanaged.passUnretained(self).toOpaque()
            let result = ioKitService.addMatchingNotification(
                port: notificationPort,
                type: kIOMatchedNotification,
                matching: matchingDict,
                callback: matchedCallback,
                refcon: selfPtr,
                iterator: &matchedIterator
            )

            guard result == KERN_SUCCESS else {
                throw USBMonitorError.failedToRegisterNotification("matched", result)
            }

            // Register for device terminated notifications (device disconnected)
            guard let terminatedDict = ioKitService.createUSBDeviceMatchingDictionary() else {
                throw USBMonitorError.failedToCreateMatchingDictionary
            }

            let terminatedCallback: IOServiceMatchingCallback = { (refcon, iterator) in
                let monitor = Unmanaged<IOKitUSBMonitor>.fromOpaque(refcon!).takeUnretainedValue()
                Task {
                    await monitor.handleDeviceTerminated(iterator: iterator)
                }
            }

            let terminatedResult = ioKitService.addMatchingNotification(
                port: notificationPort,
                type: kIOTerminatedNotification,
                matching: terminatedDict,
                callback: terminatedCallback,
                refcon: selfPtr,
                iterator: &terminatedIterator
            )

            guard terminatedResult == KERN_SUCCESS else {
                throw USBMonitorError.failedToRegisterNotification("terminated", terminatedResult)
            }

            // Consume existing matched devices to arm the notification
            await consumeExistingDevices(iterator: matchedIterator, isConnection: true)
            await consumeExistingDevices(iterator: terminatedIterator, isConnection: false)

            isRunning = true
            logger.info("✅ IOKit USB device monitoring started")
        }

        func stop() async {
            guard isRunning else { return }

            logger.info("Stopping IOKit USB device monitoring...")

            // Cancel the monitoring task
            monitoringTask?.cancel()
            monitoringTask = nil

            // Clean up IOKit resources
            if matchedIterator != 0 {
                ioKitService.objectRelease(matchedIterator)
                matchedIterator = 0
            }

            if terminatedIterator != 0 {
                ioKitService.objectRelease(terminatedIterator)
                terminatedIterator = 0
            }

            if let notificationPort = notificationPort {
                ioKitService.destroyNotificationPort(notificationPort)
                self.notificationPort = nil
            }

            runLoopSource = nil
            runLoop = nil
            isRunning = false

            logger.info("✅ IOKit USB device monitoring stopped")
        }

        func setDeviceHandler(_ handler: @escaping @Sendable (USBDeviceEvent) -> Void) async {
            self.deviceHandler = handler
            logger.debug("USB device handler set")
        }

        // MARK: - Private Methods

        private func runNotificationLoop() async {
            logger.debug("Starting IOKit notification run loop...")

            // Use the main run loop since CFRunLoop APIs are not available in async contexts
            let runLoop = CFRunLoopGetMain()
            self.runLoop = runLoop

            // Add the notification source to the main run loop
            if let source = runLoopSource {
                CFRunLoopAddSource(runLoop, source, CFRunLoopMode.defaultMode)
                logger.debug("Added IOKit notification source to main run loop")
            }

            // Keep the task alive to maintain the run loop source
            while !Task.isCancelled && isRunning {
                // Just sleep and let the run loop handle notifications
                try? await Task.sleep(for: .seconds(1))
            }

            // Remove the source from run loop before exiting
            if let source = runLoopSource {
                CFRunLoopRemoveSource(runLoop, source, CFRunLoopMode.defaultMode)
                logger.debug("Removed IOKit notification source from main run loop")
            }

            logger.debug("IOKit notification run loop ended")
        }

        private func handleDeviceMatched(iterator: io_iterator_t) async {
            logger.debug("Handling device matched notifications")
            await processDeviceIterator(iterator: iterator, isConnection: true)
        }

        private func handleDeviceTerminated(iterator: io_iterator_t) async {
            logger.debug("Handling device terminated notifications")
            await processDeviceIterator(iterator: iterator, isConnection: false)
        }

        private func consumeExistingDevices(iterator: io_iterator_t, isConnection: Bool) async {
            // Consume existing devices to arm the notification
            // Don't process them as events since they're already connected
            var device: io_service_t
            repeat {
                device = ioKitService.iteratorNext(iterator)
                if device != 0 {
                    ioKitService.objectRelease(device)
                }
            } while device != 0
        }

        private func processDeviceIterator(iterator: io_iterator_t, isConnection: Bool) async {
            var device: io_service_t

            repeat {
                device = ioKitService.iteratorNext(iterator)
                if device != 0 {
                    await processUSBDevice(device: device, isConnection: isConnection)
                    ioKitService.objectRelease(device)
                }
            } while device != 0
        }

        private func processUSBDevice(device: io_service_t, isConnection: Bool) async {
            guard let deviceInfo = extractUSBDeviceInfo(from: device) else {
                return
            }

            let event: USBDeviceEvent =
                isConnection ? .connected(deviceInfo) : .disconnected(deviceInfo)

            logger.info(
                "USB device \(isConnection ? "connected" : "disconnected")",
                metadata: [
                    "name": "\(deviceInfo.name)",
                    "vendorId": "\(deviceInfo.vendorId)",
                    "productId": "\(deviceInfo.productId)",
                    "isEdgeOS": "\(deviceInfo.isEdgeOS)",
                ]
            )

            // Call the device handler
            deviceHandler?(event)
        }

        private func extractUSBDeviceInfo(from device: io_service_t) -> USBDeviceInfo? {
            guard let properties = ioKitService.getDeviceProperties(device) else {
                logger.debug("Failed to get device properties")
                return nil
            }

            return deviceInfoExtractor.extractUSBDeviceInfo(from: properties)
        }
    }

    // MARK: - Error Types

    enum USBMonitorError: Error, LocalizedError {
        case failedToCreateNotificationPort
        case failedToCreateMatchingDictionary
        case failedToRegisterNotification(String, kern_return_t)

        var errorDescription: String? {
            switch self {
            case .failedToCreateNotificationPort:
                return "Failed to create IOKit notification port"
            case .failedToCreateMatchingDictionary:
                return "Failed to create USB device matching dictionary"
            case .failedToRegisterNotification(let type, let error):
                return "Failed to register \(type) notification: \(error)"
            }
        }
    }

#endif  // os(macOS)
