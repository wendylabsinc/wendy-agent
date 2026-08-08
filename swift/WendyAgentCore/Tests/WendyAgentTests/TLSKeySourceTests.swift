import Testing

@testable import WendyAgentCore

@Suite("TLSKeySource")
struct TLSKeySourceTests {
    @Test("software PEM backing resolves without throwing")
    func softwarePEMResolves() throws {
        // A software key is self-contained; no Secure Enclave signer is needed.
        _ = try tlsPrivateKeySource(.softwarePEM("-----BEGIN PRIVATE KEY-----"), seKey: nil)
    }

    @Test("Secure Enclave backing without a loaded key fails closed instead of crashing")
    func secureEnclaveWithoutKeyThrows() {
        // Previously this trapped with preconditionFailure, taking down the whole
        // agent process. It must now surface a catchable error so mTLS setup can
        // fail closed and log it.
        #expect(throws: TLSKeySourceError.self) {
            _ = try tlsPrivateKeySource(.secureEnclave, seKey: nil)
        }
    }
}
