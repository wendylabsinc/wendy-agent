import AppKit
import Foundation
import OSLog
import UserNotifications

/// Registers for Apple push notifications and links the resulting device token
/// to any crash-report tracking IDs the user is subscribed to, so a fix push
/// can be delivered when a new release addresses their crash. All cloud calls
/// go through `DiagnosticsClient`; this type only orchestrates the APNS side.
final class CrashFixNotifications: NSObject, UNUserNotificationCenterDelegate {
    static let shared = CrashFixNotifications()

    private let logger = Logger(
        subsystem: Bundle.main.bundleIdentifier ?? "com.wendy.WendyAgentMac",
        category: "CrashFixNotifications"
    )

    /// Crash-report tracking IDs for which the user requested fix notifications.
    /// Populated by the crash submission flow (in WendyAgentCore) before the
    /// APNS token arrives. Accessed only from `didRegister`, called on the main
    /// actor via AppDelegate, so no additional synchronisation is required.
    var pendingTrackingIDs: [String] = []

    override private init() {
        super.init()
    }

    /// Requests UNUserNotificationCenter authorisation and, if granted,
    /// registers with APNS. Must be called from the main actor (AppDelegate).
    func registerForPush() {
        UNUserNotificationCenter.current().delegate = self
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) {
            [weak self] granted, error in
            if let error {
                self?.logger.error(
                    "Push authorisation request failed: \(error.localizedDescription, privacy: .public)"
                )
            }
            guard granted else {
                self?.logger.info("Push authorisation not granted; crash-fix notifications disabled.")
                return
            }
            DispatchQueue.main.async {
                NSApplication.shared.registerForRemoteNotifications()
            }
        }
    }

    /// Called by AppDelegate when APNS returns a valid device token.
    /// Hex-encodes the token and kicks off subscription for any pending tracking IDs.
    func didRegister(deviceToken: Data) {
        let token = deviceToken.map { String(format: "%02x", $0) }.joined()
        logger.info("Registered for remote notifications; APNS token acquired.")
        Task { await DiagnosticsClient.shared.subscribeAll(trackingIDs: pendingTrackingIDs, apnsToken: token) }
    }

    // MARK: - UNUserNotificationCenterDelegate

    /// Show fix notifications even while the app is foregrounded.
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }
}

// MARK: - DiagnosticsClient

/// Thin wrapper intended to forward crash-fix subscription requests to the
/// Wendycloud diagnostics gRPC service.
///
/// **STUB — not yet wired to a live cloud channel.**
///
/// The Mac app currently has no authenticated gRPC channel to Wendycloud. Once
/// the Mac app gains an authenticated `GRPCClient` (equivalent to the cloud
/// client used in the CLI / WendyAgentCore), replace the body of `subscribe`
/// with something like:
///
/// ```swift
/// var req = Wendycloud_V1_SubscribeRequest()
/// req.trackingID = trackingID
/// req.apnsDeviceToken = apnsToken
/// _ = try? await diagnosticsClient.subscribe(req)
/// ```
///
/// Until that channel exists this actor safely no-ops while logging the intent,
/// so the rest of the push flow (token acquisition, notification display) can
/// be exercised without a live backend.
actor DiagnosticsClient {
    static let shared = DiagnosticsClient()

    private let logger = Logger(
        subsystem: Bundle.main.bundleIdentifier ?? "com.wendy.WendyAgentMac",
        category: "DiagnosticsClient"
    )

    private init() {}

    /// Subscribes a single crash-report tracking ID for APNS fix notifications.
    ///
    /// - Parameters:
    ///   - trackingID: The opaque tracking identifier returned by the crash-submit RPC.
    ///   - apnsToken: The hex-encoded APNS device token from `didRegister(deviceToken:)`.
    ///
    /// **TODO:** Wire this to `Wendycloud_V1_DiagnosticsServiceClient` once the
    /// Mac app has an authenticated cloud channel. See the type-level comment on
    /// `DiagnosticsClient` for the call pattern.
    func subscribe(trackingID: String, apnsToken: String) async {
        // STUB: No authenticated cloud channel exists in WendyAgentMac yet.
        // Logging the intent so the flow is traceable in Console.app during
        // manual testing; the CLI polling fallback remains the active delivery
        // path until the channel is wired up.
        logger.info(
            "Would subscribe trackingID \(trackingID, privacy: .public) for APNS token (redacted); stub — no cloud channel available yet."
        )
    }

    /// Convenience: iterates pending tracking IDs and calls `subscribe` for each.
    func subscribeAll(trackingIDs: [String], apnsToken: String) async {
        for id in trackingIDs {
            await subscribe(trackingID: id, apnsToken: apnsToken)
        }
    }
}
