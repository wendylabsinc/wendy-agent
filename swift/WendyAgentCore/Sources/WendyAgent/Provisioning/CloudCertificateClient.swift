import GRPCCore
import GRPCNIOTransportHTTP2
import WendyCloudGRPC

/// A signed certificate returned by the Wendy cloud `CertificateService`.
struct IssuedCertificate: Sendable {
    var certPEM: String
    var chainPEM: String
    var organizationID: Int32
    var assetID: Int32
}

/// Dials the Wendy cloud `CertificateService` to exchange a CSR for a signed
/// certificate. The work is behind a closure so tests can stub it without a
/// network; `.live` performs the real dial.
struct CloudCertificateClient: Sendable {
    var issue:
        @Sendable (_ cloudHost: String, _ csrPEM: String, _ enrollmentToken: String) async throws ->
            IssuedCertificate

    init(
        issue:
            @escaping @Sendable (_ cloudHost: String, _ csrPEM: String, _ enrollmentToken: String)
            async throws -> IssuedCertificate
    ) {
        self.issue = issue
    }

    /// `cloudHost` verbatim if it already carries a port, else `<host>:50051`.
    static func certificateServiceAddress(cloudHost: String) -> String {
        // A trailing `:<digits>` counts as a port. IPv6 literals are not used
        // for the Wendy cloud host, so a simple last-colon check suffices.
        if let colon = cloudHost.lastIndex(of: ":"),
            let port = Int(cloudHost[cloudHost.index(after: colon)...]),
            port > 0
        {
            return cloudHost
        }
        return "\(cloudHost):50051"
    }

    static let live = CloudCertificateClient { cloudHost, csrPEM, enrollmentToken in
        let address = Self.certificateServiceAddress(cloudHost: cloudHost)
        let host = String(address.prefix(while: { $0 != ":" }))
        let portString = address.drop(while: { $0 != ":" }).dropFirst()
        let port = Int(portString) ?? 50051

        let security: HTTP2ClientTransport.Posix.TransportSecurity =
            port == 443 ? .tls : .plaintext

        let transport = try HTTP2ClientTransport.Posix(
            target: .dns(host: host, port: port),
            transportSecurity: security
        )

        return try await withGRPCClient(transport: transport) { grpc in
            let client = Wendycloud_V1_CertificateService.Client(wrapping: grpc)
            var request = Wendycloud_V1_IssueCertificateRequest()
            request.pemCsr = csrPEM
            request.enrollmentToken = enrollmentToken

            let response = try await client.issueCertificate(
                request: GRPCCore.ClientRequest(message: request)
            )
            if response.hasError {
                throw RPCError(
                    code: .internalError,
                    message: "cloud certificate issuance failed: \(response.error.message)"
                )
            }
            guard response.hasCertificate else {
                throw RPCError(code: .internalError, message: "cloud returned empty certificate")
            }
            return IssuedCertificate(
                certPEM: response.certificate.pemCertificate,
                chainPEM: response.certificate.pemCertificateChain,
                organizationID: response.organizationID,
                assetID: response.assetID
            )
        }
    }
}
