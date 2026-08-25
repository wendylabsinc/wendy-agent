> **Tip:** [`wendy cloud login`](../cloud/login.md) is the recommended entry
> point for authenticating with Wendy Cloud. This page documents
> `wendy auth login`, which behaves identically and is kept for backward
> compatibility but is no longer listed in the top-level help. The advanced
> session commands (`use`, `default`, `refresh-certs`) remain under
> `wendy auth`.

For the new Cloud API authentication flow, provide your email address:

```bash
wendy cloud login --email you@example.com
```

The CLI asks `auth.dev.wendy.sh` for the email's home realm, opens that realm's authorization page, and completes authorization code + PKCE through a loopback callback. It requests the `https://cloud.dev.wendy.sh/api` audience, creates and signs an operator CSR, and stores the resulting mTLS certificate alongside the access token, rotating refresh token, and DPoP key using the platform credential store. API commands connect to `api.dev.wendy.sh:443` with this session and refresh it automatically.

The OAuth client is managed through the wendy-auth dashboard like any other interactive client; the auth service has no CLI-specific client configuration. Register a public, DPoP-bound client (the default client ID is `wendy-cli`) and allow the CLI's loopback redirect URIs. Use `--client-id` when the registered client has another ID.

Use `--auth`, `--cloud`, `--cloud-grpc`, and `--resource` to target another environment. `--issuer` accepts a complete realm issuer and skips email-based realm discovery.

The stored mTLS certificate also authorizes broker and direct-device operations. Without `--email` or `--issuer`, the command continues to use the legacy cloud-dashboard enrollment callback.

The legacy dashboard callback also prints a QR code. You can scan it with the **Wendy iOS app** to authenticate on your phone instead of the local browser.

## Multiple auth sessions

When more than one Wendy Cloud session is stored in `~/.wendy/config.json`, every cloud command resolves which session to use in the following order:

1. **`--cloud-grpc` flag** — always wins when supplied.
2. **Single stored session** — used automatically when only one session exists.
3. **Persisted default** — the session set with [`wendy auth use`](./use.md) is used when present and valid.
4. **Interactive picker** — shown in an interactive terminal when no default is set.
5. **Error** — in non-interactive environments (pipes, CI, MCP) with no default set, the command exits with an error directing you to pass `--cloud-grpc` or run `wendy auth use`.

A stale default (the named session was removed) is never silently used: the picker warns, `wendy auth default` self-clears, and non-interactive callers receive an error.
