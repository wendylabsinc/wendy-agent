# Lite grant fixture

`vector.json` is copied verbatim from service-protos commit `2b727bd`,
`conformance/lite-relay/vector.json`. It contains the complete grant, public key,
fields and public deterministic test seed as hex. These are test credentials.
CLI tests decode `record_hex` and verify grant forwarding before WendyCom.
The upstream directory contains the Go generator and firmware/Swift binary
fixtures; only the text vector is vendored here for reviewability.
