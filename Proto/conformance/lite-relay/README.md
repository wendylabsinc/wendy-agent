# Lite relay conformance vector

WDY-3512: `grant.bin` is a WLG1 grant signed with ML-DSA-65, pure mode,
empty context. `cloud-key.bin` is the raw public key. `vector.json` records
all fields and hex encodings. The deterministic seed is public test material,
never a deployment key. The principal payload is the UTF-8 string
`conformance principal descriptor`.

The generator uses the Wendy CLI module's pinned `github.com/cloudflare/circl`
implementation. From that module root, run:

```sh
go run /path/to/service-protos/conformance/lite-relay/generate.go /path/to/service-protos/conformance/lite-relay
```

Cloud tests verify the Go signature and compare Swift's encoded unsigned body.
Firmware tests verify the same record with wolfSSL and reject truncated,
modified, expired and identity-mismatched records. ML-DSA signatures produced
with randomized signing need not equal the fixture signature byte-for-byte.
