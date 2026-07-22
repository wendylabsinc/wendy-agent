import Testing

@testable import WendyAgentCore

@Suite struct DockerContainerBackendTests {
    @Test func runOptionsRenderPortsVolumesAndManagedLabels() {
        let config = WendyAppConfig(
            appId: "app",
            platform: "linux/arm64",
            entitlements: [
                WendyEntitlement(
                    type: "network",
                    mode: nil,
                    name: nil,
                    path: nil,
                    ports: [WendyPortMapping(host: 8080, container: 80)]
                ),
                WendyEntitlement(
                    type: "persist",
                    mode: nil,
                    name: "data",
                    path: "/data",
                    ports: nil
                ),
            ],
            brewfile: nil
        )
        let opts = DockerContainerBackend.runOptions(for: config, appName: "app", warn: { _ in })
        let args = opts.flatMap(\.arguments)
        #expect(args.contains("wendy.managed=true"))
        #expect(args.contains("8080:80"))
        #expect(args.contains("wendy-app-data:/data"))
        #expect(args.contains("wendy-app"))  // --name value
    }
}
