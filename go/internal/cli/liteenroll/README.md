Lite enrollment checks CA bundle compatibility before reserving a Cloud asset or
minting its enrollment credential. The current firmware uses Mbed TLS and accepts
RSA/ECDSA certificate algorithms. Go can also parse ML-DSA certificates, but that
does not make them usable by this firmware. An ML-DSA device CA hierarchy requires
a compatible PKI issuing chain or firmware with an explicitly supported verifier.
Changing only the device's leaf key to ECDSA does not change its issuer's signature.

Both discovered and explicit trust bundles use the named Mbed TLS compatibility
check. It preserves supported classical root algorithms, including older root
self-signatures, and leaves configuration unchanged on failure. This is a
preflight check; the board still verifies the complete issued certificate chain.
A future firmware capability for another verifier must select its matching
compatibility profile rather than bypassing certificate verification.
