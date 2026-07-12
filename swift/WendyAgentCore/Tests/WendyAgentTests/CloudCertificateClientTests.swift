import Testing

@testable import WendyAgentCore

@Suite("CloudCertificateClient")
struct CloudCertificateClientTests {
    @Test("address helper appends :50051 only when no port present")
    func addressHelper() {
        #expect(
            CloudCertificateClient.certificateServiceAddress(cloudHost: "cloud.example")
                == "cloud.example:50051"
        )
        #expect(
            CloudCertificateClient.certificateServiceAddress(cloudHost: "cloud.example:443")
                == "cloud.example:443"
        )
        #expect(
            CloudCertificateClient.certificateServiceAddress(cloudHost: "cloud.example:12345")
                == "cloud.example:12345"
        )
    }

    @Test("a stub client returns its issued certificate")
    func stubClient() async throws {
        let stub = CloudCertificateClient { _, csr, token in
            #expect(csr.contains("CERTIFICATE REQUEST"))
            #expect(token == "tok")
            return IssuedCertificate(certPEM: "C", chainPEM: "CH", organizationID: 7, assetID: 42)
        }
        let issued = try await stub.issue(
            "cloud.example",
            "-----BEGIN CERTIFICATE REQUEST-----",
            "tok"
        )
        #expect(issued.certPEM == "C")
        #expect(issued.organizationID == 7)
    }
}
