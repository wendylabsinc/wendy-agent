import Testing

@testable import WendyAgentCore

@Suite struct ContainerCLIBackendTests {
    @Test func specsForConfigMapNetworkAndPersist() {
        let config = WendyAppConfig(
            appId: "svc",
            platform: "linux/arm64",
            entitlements: [
                WendyEntitlement(
                    type: "network",
                    mode: nil,
                    name: nil,
                    path: nil,
                    ports: [WendyPortMapping(host: 3000, container: 3000)]
                ),
                WendyEntitlement(
                    type: "persist",
                    mode: nil,
                    name: "db",
                    path: "/data",
                    ports: nil
                ),
            ],
            brewfile: nil
        )
        let specs = ContainerCLIBackend.specs(
            for: config,
            appName: "svc",
            warn: { _ in }
        )
        #expect(specs.contains(.publishPort(host: 3000, container: 3000)))
        #expect(specs.contains(.volume(name: "wendy-svc-db", path: "/data")))
    }

    @Test func specsForNilConfigAreEmpty() {
        #expect(
            ContainerCLIBackend.specs(
                for: nil,
                appName: "svc",
                warn: { _ in }
            ).isEmpty
        )
    }
}
