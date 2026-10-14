import AVFoundation
import AppKit
import CoreBluetooth
import CoreLocation
import OSLog
import Observation
import ServiceManagement

@Observable
@MainActor
final class WelcomeAndPermissions: NSObject, CBCentralManagerDelegate, CLLocationManagerDelegate {
    enum Permission: CaseIterable, Hashable {
        case bluetooth
        case camera
        case microphone
        case location

        var title: String {
            switch self {
            case .bluetooth:
                return "Bluetooth"
            case .camera:
                return "Camera"
            case .microphone:
                return "Microphone"
            case .location:
                return "Location"
            }
        }

        var subtitle: String {
            switch self {
            case .bluetooth:
                return "Device discovery and transfers"
            case .camera:
                return "For Wendy apps that use video"
            case .microphone:
                return "For Wendy apps that use audio"
            case .location:
                return "Wi-Fi network scanning"
            }
        }

        var systemImage: String {
            switch self {
            case .bluetooth:
                return "dot.radiowaves.left.and.right"
            case .camera:
                return "camera"
            case .microphone:
                return "mic"
            case .location:
                return "location"
            }
        }
    }

    enum PermissionStatus: Equatable {
        case pending
        case allowed
        case denied
        case restricted
    }

    private static let launchAtLoginEnabledKey = "launchAtLoginEnabled"
    private static let locationRequestTimeout = Duration.seconds(60)

    private let logger = Logger(
        subsystem: Bundle.main.bundleIdentifier ?? "sh.wendy.WendyAgentMac",
        category: "WelcomeAndPermissions"
    )

    var launchAtLoginEnabled: Bool
    var locationRequestMessage: String?
    var bluetoothStatus: PermissionStatus = .pending
    var cameraStatus: PermissionStatus = .pending
    var microphoneStatus: PermissionStatus = .pending
    var locationStatus: PermissionStatus {
        // Core Location owns authorization. Register observation of this
        // projection without storing a second copy of the native status.
        self.access(keyPath: \.locationStatus)
        return self.locationStatus(for: self.locationManager.authorizationStatus)
    }
    var requestingPermission: Permission?

    private var bluetoothManager: CBCentralManager?
    private var bluetoothContinuation: CheckedContinuation<PermissionStatus, Never>?
    private let locationManager: CLLocationManager
    private let locationRequest = LocationPermissionRequest()

    override convenience init() {
        self.init(locationManager: CLLocationManager())
    }

    init(locationManager: CLLocationManager) {
        self.locationManager = locationManager
        self.launchAtLoginEnabled = Self.currentLaunchAtLoginEnabled()

        super.init()
        self.locationManager.delegate = self
        self.refreshPermissionStatuses()
    }

    var shouldShowWelcomeAndPermissions: Bool {
        self.currentBluetoothStatus() == .pending
            || self.currentCameraStatus() == .pending
            || self.currentMicrophoneStatus() == .pending
            || self.locationStatus == .pending
    }

    var canFinish: Bool {
        self.bluetoothStatus == .allowed
            && self.cameraStatus == .allowed
            && self.microphoneStatus == .allowed
            && self.locationStatus == .allowed
    }

    var isWorking: Bool {
        self.requestingPermission != nil
    }

    func configureLaunchAtLoginOnStartup() {
        self.applyLaunchAtLoginPreference(enabled: Self.currentLaunchAtLoginPreference())
    }

    func prepareForPresentation() {
        self.refresh()
    }

    func refresh() {
        self.refreshLaunchAtLoginStatus()
        self.refreshPermissionStatuses()
    }

    func setLaunchAtLoginEnabled(_ enabled: Bool) {
        self.launchAtLoginEnabled = enabled
        UserDefaults.standard.set(enabled, forKey: Self.launchAtLoginEnabledKey)
        self.applyLaunchAtLoginPreference(enabled: enabled)
    }

    func requestPermission(_ permission: Permission) async {
        guard self.requestingPermission == nil else { return }

        self.requestingPermission = permission
        defer {
            self.requestingPermission = nil
            self.refreshPermissionStatuses()
        }

        switch permission {
        case .bluetooth:
            self.bluetoothStatus = await self.requestBluetoothAccess()
        case .camera:
            self.cameraStatus = await self.requestCameraAccess()
        case .microphone:
            self.microphoneStatus = await self.requestMicrophoneAccess()
        case .location:
            await self.requestLocationAccess()
        }
    }

    func status(for permission: Permission) -> PermissionStatus {
        switch permission {
        case .bluetooth:
            return self.bluetoothStatus
        case .camera:
            return self.cameraStatus
        case .microphone:
            return self.microphoneStatus
        case .location:
            return self.locationStatus
        }
    }

    func openSystemSettings(for permission: Permission) {
        guard let settingsURL = self.systemSettingsURL(for: permission) else {
            return
        }

        guard NSWorkspace.shared.open(settingsURL) else {
            self.logger.error(
                "Failed to open System Settings for permission: \(String(describing: permission), privacy: .public)"
            )
            return
        }
    }

    func centralManagerDidUpdateState(_ central: CBCentralManager) {
        guard let bluetoothContinuation = self.bluetoothContinuation else { return }

        self.bluetoothContinuation = nil
        self.bluetoothManager = nil
        bluetoothContinuation.resume(returning: self.currentBluetoothStatus())
    }

    nonisolated func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
        // Transfer only identity across the MainActor hop, not the manager or
        // an authorization snapshot that could be stale by the time we run.
        let managerID = ObjectIdentifier(manager)
        Task { @MainActor [weak self] in
            guard let self, ObjectIdentifier(self.locationManager) == managerID else { return }

            self.logger.info(
                "Location authorization changed to native status: \(String(describing: self.locationManager.authorizationStatus), privacy: .public)"
            )
            self.refreshLocationStatus()
        }
    }

    private func refreshPermissionStatuses() {
        self.bluetoothStatus = self.currentBluetoothStatus()
        self.cameraStatus = self.currentCameraStatus()
        self.microphoneStatus = self.currentMicrophoneStatus()
        self.refreshLocationStatus()
    }

    private func refreshLocationStatus() {
        // The native property changed outside Observation. Invalidate its
        // computed projection so SwiftUI reads the source again.
        self.withMutation(keyPath: \.locationStatus) {}
        self.finishLocationRequestIfResolved()
    }

    private func finishLocationRequestIfResolved() {
        guard self.locationStatus != .pending else { return }

        self.locationRequestMessage = nil
        self.locationRequest.finish()
    }

    private func systemSettingsURL(for permission: Permission) -> URL? {
        let privacyPane: String
        switch permission {
        case .bluetooth:
            privacyPane = "Privacy_Bluetooth"
        case .camera:
            privacyPane = "Privacy_Camera"
        case .microphone:
            privacyPane = "Privacy_Microphone"
        case .location:
            privacyPane = "Privacy_LocationServices"
        }

        return URL(string: "x-apple.systempreferences:com.apple.preference.security?\(privacyPane)")
    }

    private func refreshLaunchAtLoginStatus() {
        self.launchAtLoginEnabled = Self.currentLaunchAtLoginEnabled()
    }

    private func applyLaunchAtLoginPreference(enabled: Bool) {
        let loginItemService = SMAppService.mainApp

        defer {
            self.refreshLaunchAtLoginStatus()
        }

        do {
            if enabled {
                switch loginItemService.status {
                case .enabled:
                    self.logger.info("Wendy Agent is already configured to launch at login")
                case .notRegistered, .requiresApproval, .notFound:
                    try loginItemService.register()
                    self.logLaunchAtLoginRegistrationStatus(loginItemService.status)
                @unknown default:
                    try loginItemService.register()
                    self.logLaunchAtLoginRegistrationStatus(loginItemService.status)
                }
            } else {
                switch loginItemService.status {
                case .enabled, .requiresApproval:
                    try loginItemService.unregister()
                    self.logger.info("Disabled Wendy Agent launch at login")
                case .notRegistered, .notFound:
                    break
                @unknown default:
                    try loginItemService.unregister()
                    self.logger.info("Disabled Wendy Agent launch at login")
                }
            }
        } catch {
            self.logger.error(
                "Failed to configure Wendy Agent launch at login: \(String(describing: error), privacy: .public)"
            )
        }
    }

    private func logLaunchAtLoginRegistrationStatus(_ status: SMAppService.Status) {
        switch status {
        case .enabled:
            self.logger.info("Configured Wendy Agent to launch at login")
        case .requiresApproval:
            self.logger.notice(
                "Wendy Agent launch at login requires user approval in System Settings"
            )
        case .notRegistered, .notFound:
            self.logger.warning(
                "Wendy Agent launch at login registration did not complete; status: \(String(describing: status), privacy: .public)"
            )
        @unknown default:
            self.logger.warning(
                "Wendy Agent launch at login registration returned an unknown status"
            )
        }
    }

    private static func currentLaunchAtLoginEnabled() -> Bool {
        SMAppService.mainApp.status == .enabled
    }

    private static func currentLaunchAtLoginPreference() -> Bool {
        let defaults = UserDefaults.standard
        if defaults.object(forKey: Self.launchAtLoginEnabledKey) == nil {
            return true
        }

        return defaults.bool(forKey: Self.launchAtLoginEnabledKey)
    }

    private func currentBluetoothStatus() -> PermissionStatus {
        switch CBCentralManager.authorization {
        case .allowedAlways:
            return .allowed
        case .denied:
            return .denied
        case .restricted:
            return .restricted
        case .notDetermined:
            return .pending
        @unknown default:
            return .pending
        }
    }

    private func locationStatus(for authorizationStatus: CLAuthorizationStatus) -> PermissionStatus
    {
        switch authorizationStatus {
        case .authorizedAlways:
            return .allowed
        case .denied:
            return .denied
        case .restricted:
            return .restricted
        case .notDetermined:
            return .pending
        @unknown default:
            return .pending
        }
    }

    private func currentCameraStatus() -> PermissionStatus {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized:
            return .allowed
        case .denied:
            return .denied
        case .restricted:
            return .restricted
        case .notDetermined:
            return .pending
        @unknown default:
            return .pending
        }
    }

    private func currentMicrophoneStatus() -> PermissionStatus {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized:
            return .allowed
        case .denied:
            return .denied
        case .restricted:
            return .restricted
        case .notDetermined:
            return .pending
        @unknown default:
            return .pending
        }
    }

    private func requestBluetoothAccess() async -> PermissionStatus {
        switch CBCentralManager.authorization {
        case .allowedAlways:
            return .allowed
        case .denied:
            return .denied
        case .restricted:
            return .restricted
        case .notDetermined:
            return await withCheckedContinuation { continuation in
                self.bluetoothContinuation = continuation
                self.bluetoothManager = CBCentralManager(delegate: self, queue: nil)
            }
        @unknown default:
            return .pending
        }
    }

    private func requestLocationAccess() async {
        guard self.locationStatus == .pending else { return }

        self.locationRequestMessage = nil

        let usageDescription =
            Bundle.main.object(
                forInfoDictionaryKey: "NSLocationWhenInUseUsageDescription"
            ) as? String
        let hasUsageDescription = usageDescription?.isEmpty == false
        let locationServicesEnabled = CLLocationManager.locationServicesEnabled()
        let applicationIsActive = NSApplication.shared.isActive

        self.logger.info(
            "Requesting Location permission; current status: \(String(describing: self.locationStatus), privacy: .public), usage description present: \(hasUsageDescription, privacy: .public), Location Services enabled: \(locationServicesEnabled, privacy: .public), application active: \(applicationIsActive, privacy: .public)"
        )

        guard hasUsageDescription else {
            self.logger.error(
                "Location permission request cannot open because NSLocationWhenInUseUsageDescription is missing"
            )
            self.locationRequestMessage = "Wendy cannot request Location access in this build."
            return
        }

        guard locationServicesEnabled else {
            self.logger.notice(
                "Location permission request cannot open because Location Services are disabled"
            )
            self.locationRequestMessage = "Location Services are disabled in System Settings."
            return
        }

        await self.locationRequest.wait(
            timeout: Self.locationRequestTimeout,
            onTimeout: { [weak self] in
                guard let self else { return }
                self.refreshLocationStatus()
                if self.locationStatus == .pending {
                    self.locationRequestMessage =
                        "macOS has not returned a Location decision. Try again or check System Settings."
                    self.logger.notice(
                        "Location authorization request timed out without a decision"
                    )
                }
            },
            start: {
                self.locationManager.requestWhenInUseAuthorization()
                // Returning from the request is not the user's answer. Only a
                // resolved authorization status completes the normal wait.
                self.refreshLocationStatus()
            }
        )
    }

    private func requestCameraAccess() async -> PermissionStatus {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized:
            return .allowed
        case .denied:
            return .denied
        case .restricted:
            return .restricted
        case .notDetermined:
            let granted = await AVCaptureDevice.requestAccess(for: .video)
            return granted ? .allowed : .denied
        @unknown default:
            return .pending
        }
    }

    private func requestMicrophoneAccess() async -> PermissionStatus {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized:
            return .allowed
        case .denied:
            return .denied
        case .restricted:
            return .restricted
        case .notDetermined:
            let granted = await AVCaptureDevice.requestAccess(for: .audio)
            return granted ? .allowed : .denied
        @unknown default:
            return .pending
        }
    }
}
