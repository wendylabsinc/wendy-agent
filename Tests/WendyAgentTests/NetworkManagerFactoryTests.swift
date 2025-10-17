import Foundation
import Testing

@testable import wendy_agent

@Suite("NetworkManagerFactory")
struct NetworkManagerFactoryTests {
    @Test("Factory clear cache")
    func testFactoryClearCache() async {
        let factory = NetworkConnectionManagerFactory(
            uid: "1000",
            socketPath: "/var/run/dbus/system_bus_socket"
        )

        // Clear cache should not throw
        await factory.clearCache()

        // After clearing cache, detection should happen again on next call
        // (though we can't test the actual detection without D-Bus)
    }

    @Test("Factory with invalid socket path")
    func testFactoryWithInvalidSocketPath() async {
        let factory = NetworkConnectionManagerFactory(
            uid: "1000",
            socketPath: "/invalid/socket/path"
        )

        // Should handle invalid socket gracefully
        let detectedType = await factory.getDetectedType()
        #expect(detectedType == .none)
    }

    @Test("Factory preference logic - auto")
    func testFactoryPreferenceAuto() async throws {
        let factory = NetworkConnectionManagerFactory(
            uid: "1000",
            socketPath: "/invalid/socket/path"
        )

        // With auto preference and no managers available, should throw
        await #expect(throws: NetworkConnectionError.managerNotAvailable) {
            try await factory.createNetworkManager(preference: .auto)
        }
    }

    @Test("Factory preference logic - force ConnMan unavailable")
    func testFactoryForceConnManUnavailable() async throws {
        let factory = NetworkConnectionManagerFactory(
            uid: "1000",
            socketPath: "/invalid/socket/path"
        )

        // With force ConnMan preference and ConnMan not available, should throw
        await #expect(throws: NetworkConnectionError.managerNotAvailable) {
            try await factory.createNetworkManager(preference: .forceConnMan)
        }
    }

    @Test("Factory preference logic - force NetworkManager unavailable")
    func testFactoryForceNetworkManagerUnavailable() async throws {
        let factory = NetworkConnectionManagerFactory(
            uid: "1000",
            socketPath: "/invalid/socket/path"
        )

        // With force NetworkManager preference and NetworkManager not available, should throw
        await #expect(throws: NetworkConnectionError.managerNotAvailable) {
            try await factory.createNetworkManager(preference: .forceNetworkManager)
        }
    }
}
