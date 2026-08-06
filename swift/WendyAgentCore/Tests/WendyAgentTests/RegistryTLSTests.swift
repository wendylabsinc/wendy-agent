import HummingbirdTLS
import Logging
import Testing

@testable import WendyAgentCore

@Suite("RegistryTLS")
struct RegistryTLSTests {
    private static let logger = Logger(label: "test.registry-tls")

    @Test("configuration derives the device org from the leaf and org mode from the environment")
    func configurationDerivation() throws {
        let ca = try TestPKI.makeCA()
        let device = try TestPKI.makeDeviceIdentity(org: 7, asset: 42, ca: ca)
        let certs = ProvisioningService.ProvisioningCerts(
            certPEM: device.certPEM,
            chainPEM: ca.pem,
            keyPEM: device.keyPEM
        )

        let config = RegistryTLS.makeConfiguration(
            certs: certs,
            environment: ["WENDY_MTLS_ORG_ENFORCEMENT": "strict"],
            logger: Self.logger
        )
        #expect(config.deviceOrg == 7)
        #expect(config.orgMode == .strict)

        let defaulted = RegistryTLS.makeConfiguration(
            certs: certs,
            environment: [:],
            logger: Self.logger
        )
        #expect(defaulted.orgMode == .grace)
    }

    @Test("channelConfiguration builds from a minted identity")
    func channelConfigurationSucceeds() throws {
        let ca = try TestPKI.makeCA()
        let device = try TestPKI.makeDeviceIdentity(org: 1, asset: 2, ca: ca)
        let config = RegistryTLS.Configuration(
            certPEM: device.certPEM,
            chainPEM: ca.pem,
            keyPEM: device.keyPEM,
            deviceOrg: 1,
            orgMode: .grace
        )
        let channel = try RegistryTLS.channelConfiguration(config)
        #expect(channel.customVerificationCallback != nil)
    }

    @Test("channelConfiguration throws on unusable PEM instead of degrading")
    func channelConfigurationFailsClosed() throws {
        let config = RegistryTLS.Configuration(
            certPEM: "not a certificate",
            chainPEM: "not a chain",
            keyPEM: "not a key",
            deviceOrg: 1,
            orgMode: .grace
        )
        #expect(throws: (any Error).self) {
            _ = try RegistryTLS.channelConfiguration(config)
        }
    }
}
