# Lite grant fixture

`vector.json` is copied verbatim from service-protos commit `01dc3732087d01e223e22faab0289f703318ac43`
(PR #138, WDY-3919), `conformance/lite-relay/vector.json`. It contains the complete grant, public key,
fields and public deterministic test seed as hex. These are test credentials.
The record uses the WLG3 `293 + key-ID length` encoding, a UUID device principal
and a 60-second presentation window, with separate identity and operator
public-key bindings. The WLG2 record it replaces is kept as a negative case for the
CLI's framing test at `go/internal/cli/liteclient/testdata/lite-grant-wlg2.hex`.
CLI tests decode `record_hex` and verify grant forwarding before WendyCom.
The upstream directory contains the Go generator and firmware/Swift binary
fixtures; only the text vector is vendored here for reviewability.
