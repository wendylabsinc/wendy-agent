Provisions a device using a local or self-hosted [pki-core](../../../../pki/) instance instead of Wendy Cloud. Creates an enrollment token from pki-core, then has the connected agent fetch its certificate directly from pki-core.

› **Certificate identity:** The CSR submitted during provisioning includes the
› device's authoritative Wendy identity as a URI Subject Alternative Name
› (`urn:wendy:org:‹org›:asset:‹assetID›`). The cloud certificate service
› validates this SAN against the enrollment token at issuance time.

```sh
wendy device provision --cloud 192.168.0.102:50051 --api-key <key> --name my-device
```

After provisioning, the device starts an mTLS gRPC server on port 50052 and re-advertises itself with the new port and `tls=true`. Authenticate the CLI with [`wendy auth login-local`](../auth/login-local.md) using the same pki-core to obtain a matching client certificate for mTLS connections.

**Flags:**

| Flag | Required | Description |
|------|----------|-------------|
| `--cloud` | yes | pki-core gRPC address (`host:port`) |
| `--api-key` | yes | Bearer API key for creating the enrollment token |
| `--name` | no | Human-readable device name |
| `--org` | no | Organization ID (default: `1`) |
