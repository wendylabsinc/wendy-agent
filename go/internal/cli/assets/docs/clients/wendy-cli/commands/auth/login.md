> **Tip:** [`wendy cloud login`](../cloud/login.md) is the recommended entry
> point for authenticating with Wendy Cloud. This page documents
> `wendy auth login`, which behaves identically and is kept for backward
> compatibility but is no longer listed in the top-level help. The advanced
> session commands (`use`, `default`, `refresh-certs`) remain under
> `wendy auth`.

Authenticates the CLI with Wendy Cloud. Opens a browser to the cloud dashboard, waits for the OAuth callback, generates a key pair and CSR, then issues and stores an mTLS certificate. Subsequent commands that connect to provisioned devices use this certificate automatically.

After displaying the login URL, the CLI also prints a QR code in the terminal. You can scan this QR code with the **Wendy iOS app** to authenticate on your phone instead of (or in addition to) the browser flow.

Optionally accepts `--cloud` (dashboard URL) and `--cloud-grpc` (gRPC endpoint) to point at a non-default cloud instance.

## Multiple auth sessions

When more than one Wendy Cloud session is stored in `~/.wendy/config.json`, every cloud command resolves which session to use in the following order:

1. **`--cloud-grpc` flag** — always wins when supplied.
2. **Single stored session** — used automatically when only one session exists.
3. **Persisted default** — the session set with [`wendy auth use`](./use.md) is used when present and valid.
4. **Interactive picker** — shown in an interactive terminal when no default is set.
5. **Error** — in non-interactive environments (pipes, CI, MCP) with no default set, the command exits with an error directing you to pass `--cloud-grpc` or run `wendy auth use`.

A stale default (the named session was removed) is never silently used: the picker warns, `wendy auth default` self-clears, and non-interactive callers receive an error.
