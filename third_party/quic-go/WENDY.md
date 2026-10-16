# Wendy QUIC certificate flight allowance

This is the complete Go module distribution of quic-go v0.62.0, under the
unmodified upstream MIT license in LICENSE. WENDY-PROVENANCE.json records the
module archive hash and upstream commit. WENDY.patch is the complete upstream
code delta; all other upstream files are byte-for-byte identical to that archive.
The top-level WendyOS go.mod replaces this dependency with this directory.

Cloud v2 device enrollment currently presents a legitimate 23,740-byte DER
certificate chain (leaf plus three non-root issuers). TLS framing increases its
CRYPTO flight beyond quic-go's 16 KiB receive offset limit. Raise the bound to
64 KiB, keeping a finite bound and every certificate/authentication check.
No issuer is removed, no trust anchor is added, and no verification is bypassed.
The constant applies to each encryption-level CRYPTO stream, including Initial;
the additional maximum accepted offset is 48 KiB per stream, not a preallocation.

To update: obtain the desired official module archive, verify its Go module
checksum, replace this snapshot, and reapply/reassess the one-line patch. Update
provenance and keep the unmodified upstream license. Delete this replacement
when upstream provides a sufficient bound or a supported configurable limit.
Do not patch the shared Go module cache.

From WendyOS root, run the application handshake regression:

    go test ./go/internal/agent/localmesh -run TestCloudV2LargeChainQUIC -count=1

From this directory, run the upstream offset-boundary and crypto stream tests:

    go test . -run 'TestCryptoStream|TestInitialCryptoStream' -count=1

The application regression uses genuine signed ECDSA chains with noncritical
padding to reproduce the production chain size without embedding production
certificates or private keys. It exercises full QUIC mutual authentication,
principal pinning, required intermediate delivery, and an oversized rejection.
Existing ML-DSA verification tests continue to cover the cryptographic verifier.
