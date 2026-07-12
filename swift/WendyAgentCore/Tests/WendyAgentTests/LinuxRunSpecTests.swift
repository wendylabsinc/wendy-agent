import Testing

@testable import WendyAgentCore

@Suite struct LinuxRunSpecTests {
    @Test func mapsNetworkNone() {
        let ents = [
            WendyEntitlement(type: "network", mode: "none", name: nil, path: nil, ports: nil)
        ]
        let specs = LinuxRunSpecBuilder.specs(from: ents, appName: "app", warn: { _ in })
        #expect(specs == [.networkNone])
    }

    @Test func mapsPublishedPorts() {
        let ents = [
            WendyEntitlement(
                type: "network",
                mode: nil,
                name: nil,
                path: nil,
                ports: [WendyPortMapping(host: 8080, container: 80)]
            )
        ]
        let specs = LinuxRunSpecBuilder.specs(from: ents, appName: "app", warn: { _ in })
        #expect(specs == [.publishPort(host: 8080, container: 80)])
    }

    @Test func mapsPersistVolumeWithNamespacedName() {
        let ents = [
            WendyEntitlement(
                type: "persist",
                mode: nil,
                name: "data",
                path: "/var/data",
                ports: nil
            )
        ]
        let specs = LinuxRunSpecBuilder.specs(from: ents, appName: "app", warn: { _ in })
        #expect(specs == [.volume(name: "wendy-app-data", path: "/var/data")])
    }

    @Test func warnsOnHardwareEntitlementAndEmitsNoSpec() {
        var warnings: [String] = []
        let ents = [WendyEntitlement(type: "gpu", mode: nil, name: nil, path: nil, ports: nil)]
        let specs = LinuxRunSpecBuilder.specs(
            from: ents,
            appName: "app",
            warn: { warnings.append($0) }
        )
        #expect(specs.isEmpty)
        #expect(warnings.count == 1)
        #expect(warnings[0].contains("gpu"))
    }

    @Test func managedNameIsPrefixed() {
        #expect(managedContainerName(for: "myapp") == "wendy-myapp")
    }
}
