/// The agent's embedded registry listens on two ports (see `AgentImageRegistry`):
///
/// - **Push** (`pushPort`, all interfaces): where the CLI pushes images —
///   plain HTTP while unprovisioned, HTTPS with client-certificate
///   verification once provisioned. This is the port baked into the image
///   references the CLI sends (`localhost:5555/<app>:latest`).
/// - **Pull** (`pullPort`, loopback only, always plain HTTP): where the
///   on-device Linux container backends (Apple `container`, Docker) pull
///   from. Backends can't present client certificates, so they must never
///   hit the push listener once it speaks TLS.
///
/// `rewriteForLocalPull` bridges the two: CLI-sent references keep the push
/// port for a stable client-facing contract (and for apps installed before
/// the split), and are rewritten to the pull listener at the moment the
/// agent hands them to a backend.
enum LocalRegistryRef {
    static let pushPort = 5555
    static let pullPort = 5556
    static let pullHost = "127.0.0.1"

    /// Authorities (the part of an image reference before the first `/`) that
    /// designate this device's own push listener.
    private static let localPushAuthorities: Set<String> = [
        "localhost:\(pushPort)",
        "127.0.0.1:\(pushPort)",
        "[::1]:\(pushPort)",
    ]

    /// Rewrites a device-local push-port image reference
    /// (`localhost:5555/<repo>...`) to the loopback pull listener
    /// (`127.0.0.1:5556/<repo>...`). Any other reference — remote registries,
    /// `sha256:` digests, legacy bare binary names — is returned unchanged.
    /// Tags and `@sha256:` digest suffixes after the first `/` are preserved.
    static func rewriteForLocalPull(_ ref: String) -> String {
        guard let slash = ref.firstIndex(of: "/") else { return ref }
        let authority = String(ref[..<slash])
        guard localPushAuthorities.contains(authority) else { return ref }
        return "\(pullHost):\(pullPort)\(ref[slash...])"
    }
}
