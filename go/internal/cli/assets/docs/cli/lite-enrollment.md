# Enroll Wendy Lite with pki-core

Build this CLI with Go 1.27 or newer, then use an OIDC operator session for the
Wendy development PKI:

```sh
cd go
go build -o ./wendy ./cmd/wendy
./wendy cloud login --email YOU@YOUR_ORGANIZATION
./wendy cloud enroll-device \
  --device wendy-lite:/dev/cu.usbmodemXXXX \
  --name lite-desk \
  --broker-port 5055
```

This is the same `cloud enroll-device` command used for WendyOS. The selected
device determines the protocol: agent gRPC for WendyOS, or WendyCom over the selected USB, LAN, or Bluetooth connection for
Lite. Omit `--device` to use the normal picker, or use a device ID shown by
`wendy discover`. The hidden `wendy device enroll` alias uses the same dispatch.

The board needs Wi-Fi and the PKI-enabled firmware from
[wendy-lite#49](https://github.com/wendylabsinc/wendy-lite/pull/49) and its dedicated
identity partition. Trust bundles can be provisioned during enrollment, so
the firmware does not need deployment-specific roots. The CLI checks firmware
support before minting a token. Firmware must support enrollment on the selected
transport; older firmware may accept bootstrap commands only over USB. After
reboot, completion checks reuse the selected transport and its authentication.
Cloud enrollment does not replace the firmware's LAN or Bluetooth credentials.

Signed time defaults to [Sigstore's public RFC 3161 service](https://docs.sigstore.dev/cosign/verifying/timestamps/)
at `https://timestamp.sigstore.dev/api/v1/timestamp`. The CLI bundles Sigstore's
TSA root, discovers device roots from the selected PKI's EST endpoint, and obtains
HTTPS roots from connections verified by the enrolling computer. No time or root
flags are needed with a deployment that supports EST discovery.

Update older Lite firmware before using this default. The verifier must support
Sigstore's P-384 timestamp signer and RFC 3161 signing-certificate issuer details.
Cloudflare's Roughtime service uses a different protocol and is not supported by
this firmware.

For custom device trust, supply `--device-roots device-ca-bundle.pem` and
`--https-roots server-ca-bundle.pem` together. The default TSA root is still
included, and the CLI adds the verified HTTPS root for Sigstore. To use a custom
RFC 3161 service, pass `--time-url https://time.example/v1/time` and provide
`--tsa-roots tsa-ca-bundle.pem` with the device and HTTPS bundles. Obtain custom
bundles from the PKI administrator. Firmware with all three custom bundles pinned
at build time can omit the file flags.

Device/operator and TSA chains must be compatible with mbedTLS throughout;
a P-256 leaf beneath an ML-DSA issuer is insufficient. The TSA must sign with
ECDSA P-256 or P-384 and SHA-256. The HTTPS bundle must cover the broker too.

The firmware stores bundles with enrollment configuration and uses them after
reboot. Each bundle supports at most eight CA certificates and 16 KiB. Bundles
plus the signed-time response must fit within 64 KiB. Normal Wi-Fi configuration
updates preserve enrollment. Replacing or erasing the entire configuration removes
its bundles; changing an enrolled board's trust through the CLI requires operator
recovery. This does not introduce remote trust rotation.

The CLI obtains the board's nonce and a signed RFC 3161 timestamp. The board
verifies the TSA chain, signature, nonce, imprint and persisted rollback floor. The CLI then asks
Cloud's `DeviceEnrollmentService.EnrollDevice` for a Class C credential. Both the Cloud
RPC and PKI enrollment artifact are signed with the operator's existing identity.
Cloud reserves the tenant/device asset in PostgreSQL. The single-use token and
public setup data go to the board over the selected connection; its private key is generated
on-device. The CLI reboots the board and waits for certificate installation.

The CSR URL derives from the selected session's `PKIEndpoint`. The default time
service is independent of that deployment.
Self-hosted deployments can pass `--csr-url https://csr.example/v1/TENANT_UUID`
and `--time-url https://time.example/v1/time`. The broker hostname defaults
to the same devices hostname used by wendy-agent for the selected Cloud session.
The Lite control connection uses port 5055 by default; that listener must be exposed on
the devices hostname. Override it with `--broker-host` for a separate broker.
Use `--cloud-grpc` to select a particular Cloud endpoint/session.

A reserved asset or uploaded configuration is not a successful enrollment.
Failures report the reserved asset ID; rerunning does not silently recover an
expired or existing identity. After enrollment, `wendy cloud discover` shows
presence once the board completes mTLS and the broker's presence checks.

After enrollment, select the online Lite asset through `wendy cloud discover`.
The CLI opens a broker-authorized relay using the selected Cloud session, then
uses its same-tenant operator certificate to authenticate directly to the device.
It verifies the device certificate against the enrolled PKI device name and tenant
before sending WendyCom. Operator credentials must already be installed locally. Keep the ML-DSA login
identity for Cloud administration and enrollment authorization. For Lite TLS and
ES256 request signing, provision a P-256 identity with the same operator SPIFFE URI:

```sh
export WENDY_LITE_OPERATOR_CERT=/path/to/operator-leaf-and-chain.pem
export WENDY_LITE_OPERATOR_KEY=/path/to/operator-private-key.pem
```

The certificate file must include the compatible CA chain. The CLI rejects an
identity that differs from the selected operator. These files are issued by PKI;
they are not self-signed credentials generated by the CLI.

The broker authenticates the device's initial mTLS control connection, requests an
upgrade, and closes that TLS session without closing TCP. It then forwards opaque
bytes between the CLI and the same device socket. The device becomes the TLS
server and requires an operator certificate. The firmware reconnects its control
connection after each handoff so another session can connect while the current
one remains active. Relay payloads contain TLS bytes, with WendyCom framing inside
the encrypted session. Older bare-protobuf relay clients are incompatible.

This requires the coordinated firmware and broker update in
[cloud#753](https://github.com/wendylabsinc/cloud/pull/753), tracked by WDY-3512.

Read the firmware console with:

```sh
wendy device logs --device wendy-lite:/dev/cu.usbmodemXXXX
```

This streams buffered and live output without blocking firmware tasks. Ctrl-C
detaches; `--json` reports console chunks with `data`, `stderr`, and `gap` fields.
WendyOS app/service/severity filters, `--tail`, and `--no-follow` are not supported
by the Lite console. USB was tested on the XIAO ESP32-S3; LAN and BLE use the
provider's existing authenticated connection paths.
