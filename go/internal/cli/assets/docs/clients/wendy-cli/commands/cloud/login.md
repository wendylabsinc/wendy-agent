# `wendy cloud login`

Authenticates the CLI with Wendy Cloud. This is the primary login entry point.

## Usage

```sh
wendy cloud login
wendy cloud login --email you@example.com
wendy cloud login --issuer https://auth.wendy.dev/realms/<realm>
wendy cloud login --service-account ./wendy-service-account.json
```

## Description

`wendy cloud login` uses the same implementation as
[`wendy auth login`](../auth/login.md). It defaults to production Wendy Cloud
and opens `https://auth.wendy.dev/authorize`, where the browser collects your
email and organization. With `--email`, it discovers your realm before opening
the browser; `--issuer` selects a realm directly. The CLI completes authorization
code + PKCE through a loopback callback, obtains an operator certificate from
pki-core, and stores it with a refreshable Cloud API session. Subsequent
commands use the certificate automatically.

Use `--development` to select the development environment, or `--legacy` to
use the old dashboard flow at `cloud.wendy.sh`.

`--api-key` selects local authentication. `--service-account` signs in
headlessly as a wendy-auth service account and also targets production by
default. See [`wendy auth login`](../auth/login.md#service-account-login).

`wendy auth login` remains functional for backward compatibility but is no
longer listed in the top-level help. See [`wendy auth login`](../auth/login.md)
for the full flag reference and multi-session behaviour.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--email` | `""` | Email address for explicit realm discovery (OIDC flow). |
| `--issuer` | `""` | Complete realm issuer URL; skips email-based realm discovery. |
| `--service-account` | `""` | Service-account key file for headless login; `WENDY_SERVICE_ACCOUNT_KEY` may hold its contents instead. |
| `--cloud` | `""` | Dashboard URL override; uses `https://cloud.wendy.dev` by default. |
| `--cloud-grpc` | `""` | gRPC endpoint override; uses `api.wendy.dev:443` by default. |

## See also

- [`wendy cloud logout`](./logout.md)
- [`wendy cloud status`](./status.md)
- [`wendy auth use`](../auth/use.md) — advanced multi-session management
