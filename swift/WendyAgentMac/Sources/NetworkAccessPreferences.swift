import Foundation

@MainActor
struct NetworkAccessPreferences {
    private let defaults: UserDefaults
    private static let key = "allowNetworkAccess"

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    var hasChosenNetworkAccess: Bool {
        self.defaults.object(forKey: Self.key) != nil
    }

    // Missing preferences (including upgrades from older versions) stay local.
    var allowsNetworkAccess: Bool {
        get { self.defaults.bool(forKey: Self.key) }
        nonmutating set { self.defaults.set(newValue, forKey: Self.key) }
    }
}
