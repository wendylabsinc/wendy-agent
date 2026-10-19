# Lite grant fixture

`vector.json` is copied verbatim from service-protos commit `d9bc039f16f0368237b4dba0d964b44a16deca19`,
`conformance/lite-relay/vector.json`. It contains the complete grant, public key,
fields and public deterministic test seed as hex. These are test credentials.
The record uses the current `309 + key-ID length` encoding and a 60-second
presentation window, with separate identity and operator public-key bindings.
CLI tests decode `record_hex` and verify grant forwarding before WendyCom.
The upstream directory contains the Go generator and firmware/Swift binary
fixtures; only the text vector is vendored here for reviewability.
