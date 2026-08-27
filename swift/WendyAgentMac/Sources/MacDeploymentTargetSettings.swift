import Foundation

/// Controls whether the macOS app exposes this Mac as a Wendy deployment target.
///
/// The preference is deliberately opt-in. The environment override is intended
/// for development and automated tests that need deterministic startup behavior.
@MainActor
struct MacDeploymentTargetSettings {
    static let enabledKey = "macDeploymentTargetEnabled"
    static let environmentKey = "WENDY_MAC_DEPLOYMENT_TARGET"

    private let userDefaults: UserDefaults
    private let environment: [String: String]

    init(
        userDefaults: UserDefaults = .standard,
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) {
        self.userDefaults = userDefaults
        self.environment = environment
    }

    var isEnabled: Bool {
        self.environmentOverride ?? self.userDefaults.bool(forKey: Self.enabledKey)
    }

    var isUserEditable: Bool {
        self.environmentOverride == nil
    }

    func setEnabled(_ enabled: Bool) {
        guard self.isUserEditable else { return }
        self.userDefaults.set(enabled, forKey: Self.enabledKey)
    }

    private var environmentOverride: Bool? {
        guard let value = self.environment[Self.environmentKey] else { return nil }

        switch value.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() {
        case "1", "true", "yes", "on":
            return true
        case "0", "false", "no", "off":
            return false
        default:
            return nil
        }
    }
}
