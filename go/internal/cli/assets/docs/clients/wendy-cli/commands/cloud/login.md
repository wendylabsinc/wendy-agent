# `wendy cloud login`

Authenticates the CLI with Wendy Cloud. This is the primary login entry point.

## Usage

```sh
wendy cloud login
wendy cloud login --email you@example.com
wendy cloud login --issuer https://auth.wendy.dev/realms/<realm>
wendy cloud login --service-account ./wendy-service-account.json
wendy cloud login --legacy
```

## Description

`wendy cloud login` is identical to [`wendy auth login`](../auth/login.md).
The current CLI defaults to the production Cloud v2 browser sign-in. The browser
collects your email and organization, and the CLI obtains an operator certificate
and saves a refreshable Cloud API session. With `--email`, it first discovers
your realm; if no organization is pinned for that email, it continues with the
universal sign-in. `--issuer` selects a realm directly.

For existing Cloud v1 devices, use `wendy cloud login --legacy` to open
`cloud.wendy.sh`. This flag remains supported even though it is hidden from
ordinary CLI help. It cannot be combined with `--email`, `--issuer`,
`--service-account`, or `--api-key`. See the [Cloud guide](/docs/cloud) for
Cloud v2 and Legacy tabs, and [Moving to Cloud v2](/docs/cloud-migration)
before changing an existing device's enrollment.

After signing in, run `wendy cloud status`. If another context is still active,
select the intended one with `wendy auth use <context-name>`.

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
| `--legacy` | `false` | Use the Cloud v1 dashboard and broker defaults. Explicit compatibility flag, hidden from ordinary CLI help. |

## See also

- [`wendy cloud logout`](./logout.md)
- [`wendy cloud status`](./status.md)
- [`wendy auth use`](../auth/use.md) — advanced multi-session management
