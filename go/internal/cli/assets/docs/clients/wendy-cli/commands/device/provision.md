Provisions a device using a local or self-hosted [pki-core](../../../../pki/) instance instead of Wendy Cloud. Creates an enrollment token via the pki-core `CertificateService`, then calls `StartProvisioning` on the connected agent, which fetches its certificate directly from pki-core.

```sh
wendy device provision --cloud 192.168.0.102:50051 --api-key <key> --name my-device
```

After provisioning, the device starts an mTLS gRPC server on port 50052 and updates its Avahi advertisement to reflect the new port and `tls=true`. Authenticate the CLI with [`wendy auth login-local`](../auth/login-local.md) using the same pki-core to obtain a matching client certificate for mTLS connections.

**Flags:**

| Flag | Required | Description |
|------|----------|-------------|
| `--cloud` | yes | pki-core gRPC address (`host:port`) |
| `--api-key` | yes | Bearer API key for `CreateAssetEnrollmentToken` |
| `--name` | no | Human-readable device name |
| `--org` | no | Organization ID (default: `1`) |
