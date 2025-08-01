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

        init(logger: Logger) {
            self.logger = logger
        }

        func start() async throws {
            guard !isRunning else {
                logger.debug("IOKit USB monitor already running")
                return
            }

            logger.info("Starting IOKit USB device monitoring...")

            // Create notification port
            notificationPort = IONotificationPortCreate(kIOMainPortDefault)
            guard let notificationPort = notificationPort else {
                throw USBMonitorError.failedToCreateNotificationPort
            }

            // Get the run loop source
            if let unmanagedSource = IONotificationPortGetRunLoopSource(notificationPort) {
                runLoopSource = unmanagedSource.takeUnretainedValue()
            } else {
                throw USBMonitorError.failedToCreateNotificationPort
            }

            // Create matching dictionary for USB devices
            guard let matchingDict = IOServiceMatching(kIOUSBDeviceClassName) else {
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
            let result = IOServiceAddMatchingNotification(
                notificationPort,
                kIOMatchedNotification,
                matchingDict,
                matchedCallback,
                selfPtr,
                &matchedIterator
            )

            guard result == KERN_SUCCESS else {
                throw USBMonitorError.failedToRegisterNotification("matched", result)
            }

            // Register for device terminated notifications (device disconnected)
            guard let terminatedDict = IOServiceMatching(kIOUSBDeviceClassName) else {
                throw USBMonitorError.failedToCreateMatchingDictionary
            }

            let terminatedCallback: IOServiceMatchingCallback = { (refcon, iterator) in
                let monitor = Unmanaged<IOKitUSBMonitor>.fromOpaque(refcon!).takeUnretainedValue()
                Task {
                    await monitor.handleDeviceTerminated(iterator: iterator)
                }
            }

            let terminatedResult = IOServiceAddMatchingNotification(
                notificationPort,
                kIOTerminatedNotification,
                terminatedDict,
                terminatedCallback,
                selfPtr,
                &terminatedIterator
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
                IOObjectRelease(matchedIterator)
                matchedIterator = 0
            }

            if terminatedIterator != 0 {
                IOObjectRelease(terminatedIterator)
                terminatedIterator = 0
            }

            if let notificationPort = notificationPort {
                IONotificationPortDestroy(notificationPort)
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
                device = IOIteratorNext(iterator)
                if device != 0 {
                    IOObjectRelease(device)
                }
            } while device != 0
        }

        private func processDeviceIterator(iterator: io_iterator_t, isConnection: Bool) async {
            var device: io_service_t

            repeat {
                device = IOIteratorNext(iterator)
                if device != 0 {
                    await processUSBDevice(device: device, isConnection: isConnection)
                    IOObjectRelease(device)
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
            // Get device properties
            var properties: Unmanaged<CFMutableDictionary>?
            let result = IORegistryEntryCreateCFProperties(
                device,
                &properties,
                kCFAllocatorDefault,
                0
            )

            guard result == KERN_SUCCESS, let props = properties?.takeRetainedValue() else {
                logger.debug("Failed to get device properties")
                return nil
            }

            let propsDict = props as NSDictionary

            // Extract vendor ID
            guard let vendorIdNum = propsDict["idVendor"] as? NSNumber else {
                logger.debug("No vendor ID found")
                return nil
            }
            let vendorId = String(format: "%04X", vendorIdNum.uint16Value)

            // Extract product ID
            guard let productIdNum = propsDict["idProduct"] as? NSNumber else {
                logger.debug("No product ID found")
                return nil
            }
            let productId = String(format: "%04X", productIdNum.uint16Value)

            // Extract device name (try multiple possible keys)
            let deviceName =
                (propsDict["USB Product Name"] as? String)
                ?? (propsDict["kUSBProductString"] as? String)
                ?? (propsDict["Product Name"] as? String) ?? "Unknown USB Device"

            // Create USBDevice to check if it's EdgeOS
            let usbDevice = USBDevice(
                name: deviceName,
                vendorId: Int(vendorId, radix: 16) ?? 0,
                productId: Int(productId, radix: 16) ?? 0
            )

            return USBDeviceInfo(from: usbDevice)
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
