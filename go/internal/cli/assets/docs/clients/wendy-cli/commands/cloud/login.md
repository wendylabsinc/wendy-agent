# `wendy cloud login`

Authenticates the CLI with Wendy Cloud. This is the primary login entry point.

## Usage

```sh
wendy cloud login
wendy cloud login --cloud <dashboard-url> --cloud-grpc <grpc-endpoint>
```

## Description

`wendy cloud login` is identical to [`wendy auth login`](../auth/login.md) — it
reuses the same implementation. It opens a browser to the cloud dashboard, waits
for the OAuth callback, generates a key pair and CSR, then issues and stores an
mTLS certificate that subsequent commands use automatically.

`wendy auth login` remains functional for backward compatibility but is no
longer listed in the top-level help. See [`wendy auth login`](../auth/login.md)
for the full flag reference and multi-session behaviour.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cloud` | `""` | Dashboard URL of a non-default cloud instance. |
| `--cloud-grpc` | `""` | gRPC endpoint of a non-default cloud instance. |

## See also

- [`wendy cloud logout`](./logout.md)
- [`wendy cloud status`](./status.md)
- [`wendy auth use`](../auth/use.md) — advanced multi-session management
