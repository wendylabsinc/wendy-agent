Lite enrollment checks CA bundle compatibility before reserving a Cloud asset or
minting its enrollment credential. Firmware defaults to Mbed TLS and accepts
RSA/ECDSA certificate algorithms. Firmware built with `CONFIG_WENDY_PKI_WOLFSSL=y`
also advertises ML-DSA-44/65/87 certificate verification. The CLI permits those
algorithms only after the selected board explicitly reports that capability.
Older firmware and an absent or false capability retain the Mbed TLS restrictions.
Changing only the device's leaf key to ECDSA does not change its issuer's signature.

Both discovered and explicit trust bundles use the negotiated compatibility
profile. Each profile still checks PEM syntax, CA status, validity, size and
certificate algorithms. ML-DSA support does not permit Ed25519 or DSA. Failed
validation leaves configuration unchanged. The fresh enrollment challenge is
checked again before minting in case the board's capabilities changed. The board
still verifies the complete issued certificate chain.
