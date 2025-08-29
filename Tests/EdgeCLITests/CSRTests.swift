import ArgumentParser
import Foundation
import Testing
import Crypto
import X509

import EdgeSDK

fileprivate func makeAuthority() throws -> Authority {
    try Authority(
        privateKey: Certificate.PrivateKey(Curve25519.Signing.PrivateKey()),
        name: DistinguishedName {
            CommonName("org-id")
            CommonName("project-id")
            CommonName("developer-id")
            CommonName("machine-id")
        }
    )
}

fileprivate func makeAgent(agentId: String = "agent-id") throws -> Agent.Unprovisioned {
    try Agent.Unprovisioned(
        privateKey: Certificate.PrivateKey(Curve25519.Signing.PrivateKey()),
        name: DistinguishedName {
            CommonName("org-id")
            CommonName("project-id")
            CommonName(agentId)
        }
    )
}

@Suite("CSRTests")
struct CSRTests {
    
    @Test("CSR Authority Signs Agent")
    func signAgent() throws {
        let authority = try makeAuthority()
        let agent = try makeAgent()
        
        let certificate = try authority.sign(
            agent.csr,
            validUntil: Date().addingTimeInterval(3600)
        )
        
        let _ = try agent.receiveSignedCertificate(certificate)
    }
    
    @Test("Agent Rejects Public-Key Mismatch")
    func agentRejectsMismatch() throws {
        #expect(throws: Agent.ProvisioningError.publicKeyMismatch) {
            let authority = try makeAuthority()
            let agent = try makeAgent(agentId: "1")
            let otherAgent = try makeAgent(agentId: "2")
            
            _ = try agent.receiveSignedCertificate(
                try authority.sign(
                    otherAgent.csr,
                    validUntil: Date().addingTimeInterval(3600)
                )
            )
        }
    }
    
    @Test("Agent Rejects Not Yet Valid Public-Key")
    func agentRejectsNotYetValidCert() throws {
        #expect(
            throws: Agent.ProvisioningError.certificateNotValidYet
        ) {
            let authority = try makeAuthority()
            let agent = try makeAgent(agentId: "1")
            
            let cert = try authority.sign(
                agent.csr,
                validFrom: Date().addingTimeInterval(60),
                validUntil: Date().addingTimeInterval(3600)
            )
            _ = try agent.receiveSignedCertificate(cert)
        }
    }
    
    @Test("Agent Rejects Expired Public-Key")
    func agentRejectsExpiredCert() throws {
        #expect(
            throws: Agent.ProvisioningError.certificateNotValidAnymore
        ) {
            let authority = try makeAuthority()
            let agent = try makeAgent(agentId: "1")
            
            let cert = try authority.sign(
                agent.csr,
                validFrom: Date().addingTimeInterval(-60),
                validUntil: Date().addingTimeInterval(-1)
            )
            _ = try agent.receiveSignedCertificate(cert)
        }
    }
}

import EdgeAgentGRPC
import NIOSSL
import NIOCore
import NIOPosix
import GRPCHealthService
import GRPCNIOTransportHTTP2
import GRPCServiceLifecycle

@Suite("MTLS Tests")
struct MTLSTests {
    @Test func testMutualAuth() async throws {
        let authority = try makeAuthority()
        let agent = try makeAgent()
        
        let privateKey = agent.privateKey
        let certificate = try authority.sign(
            agent.csr,
            validUntil: Date().addingTimeInterval(3600)
        )
        
        let _ = try agent.receiveSignedCertificate(certificate)
        
        try await confirmation { confirm in
            func runClient() async throws {
                let clientPrivateKeyPEM = try privateKey.serializeAsPEM()
                try await withGRPCClient(
                    transport: .http2NIOPosix(
                        target: .dns(host: "localhost", port: 8123),
                        transportSecurity: .mTLS(
                            certificateChain: [],
                            privateKey: .bytes(clientPrivateKeyPEM.derBytes, format: .der),
                            configure: { tls in
                                ()
                            }
                        )
                    )
                ) { client in
                    confirm()
                    // Keep open for 1 sec
                    try await Task.sleep(for: .seconds(1))
                }
            }
            
            let agentPrivateKeyPEM = try privateKey.serializeAsPEM()
            try await withGRPCServer(
                transport: .http2NIOPosix(
                    address: .ipv6(host: "::", port: 8123),
                    transportSecurity: .mTLS(
                        certificateChain: [],
                        privateKey: .bytes(agentPrivateKeyPEM.derBytes, format: .der)
                    )
                ),
                services: []
            ) { server async throws -> Void in
                try await runClient()
            }
        }
    }
}
