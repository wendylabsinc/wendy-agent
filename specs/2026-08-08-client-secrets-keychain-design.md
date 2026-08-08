# Client Secrets in the macOS Keychain

**Date:** 2026-08-08
**Status:** Approved (design), implementation pending
**Branch:** `ed/client-key-keychain` (stacked on `ed/tls-session-resumption`, PR #1612)

## Problem

`~/.wendy/config.json` stores two secrets in plaintext: the per-org ML-DSA
client private key (`CertificateInfo.PemPrivateKey`) and the cloud API bearer
token (`AuthConfig.APIKey`). Any process or backup that can read the file owns
the user's device-fleet identity. PR #1612 already established the Keychain
plumbing (`security` CLI subprocess, stdin-only writes) for TLS session
tickets; this PR extends that posture to the credentials themselves.

Certificates and chains are public material and stay in the JSON.

## Decisions (made during design review)

- **Scope:** both secrets move — private key AND API token.
- **Migration:** automatic and silent on macOS; a one-line notice prints the
  first time. Trade-off accepted: after migration an OLDER wendy binary
  reading the config fails mTLS/cloud auth until `wendy auth login` is re-run.
- **Non-darwin:** unchanged (inline plaintext) until a Secret-Service/DPAPI
  backend exists; `0600` file posture matches the platform norm.

## Design

### 1. Shared Keychain store — promote from `tlscache`

The `security`-CLI runner and keychain store move from
`go/internal/cli/tlscache` into a new shared package
`go/internal/cli/secretstore` (exported: a `Store` interface with
`Get/Put/Delete`, the keychain implementation, and the swappable runner for
test fakes). `tlscache` consumes the shared keychain store; its file backend,
`WENDY_TLS_SESSION_STORE` env, and `off` semantics stay in `tlscache`
untouched — tickets are droppable, credentials are not, so the two features
keep separate policy knobs.

Credential items: service **`wendy-credentials`**, account
`<kind>-<sha16>` where `kind ∈ {key, token}` and `sha16` is the first 16 hex
chars of `SHA256(cloudGRPC|orgID|userID)` for keys, and `SHA256(cloudGRPC|orgID)`
for tokens. Tokens are keyed by org as well as endpoint because `AddAuth`
deliberately keeps one auth entry per `(cloudGRPC, orgID)` pair — several
orgs can share one endpoint (e.g. multiple orgs on the production cloud) —
so a per-endpoint-only account would let a second org's token overwrite the
first org's Keychain item on save. Deterministic accounts mean re-login for
the same identity overwrites the same item. Writes ride stdin via
`security -i` (never argv), mirroring PR #1612.

Honest posture (same as the ticket store): the item ACL trusts
`/usr/bin/security`, so any same-user process reads it promptlessly — the
gain over a `0600` file is at-rest encryption while the keychain is locked
plus backup exclusion, not same-user malware resistance.

### 2. Reference format + lazy accessors

Secret fields in `config.json` hold either a real value (pre-migration /
non-darwin / `WENDY_SECRET_STORE=file`) or a reference:

```
keychain:v1:<account>
```

Reads move to accessors on the config types:

- `(*CertificateInfo).PrivateKeyPEM() (string, error)`
- `(*AuthConfig).BearerToken() (string, error)`
- `(*CertificateInfo).HasPrivateKey() bool`, `(*AuthConfig).HasAPIKey() bool`
  (non-empty field — reference or inline both count) for the
  presence-check-only sites.

Accessor behavior: an inline value returns as-is; a `keychain:v1:` reference
resolves through the store, **memoized per process** (package-level map +
mutex — 54 `config.Load()` call sites and repeated loads within one command
must cost at most one `security` subprocess per distinct secret). An
unrecognized `keychain:` version fails with the same actionable error as a
failed read:

> credential is stored in the macOS Keychain but could not be read
> (keychain locked?): <cause>
> Unlock with 'security unlock-keychain', or re-run 'wendy auth login' with
> WENDY_SECRET_STORE=file to keep credentials in config.json.

All ~20 `PemPrivateKey` and ~3 `APIKey` consumer sites migrate to the
accessors mechanically. Commands that never touch a secret never touch the
Keychain.

### 3. Migration lives in `Save()`

`config.Save()` dehydrates before writing: on darwin with the default store,
any secret field holding a real value is `Put` into the Keychain and the
field is rewritten to its reference. This makes migration automatic wherever
config is saved. Two triggers cover everyone:

- A root-command hook (alongside the existing ambient checks) detects
  plaintext secrets on darwin and runs `Load`+`Save` once, printing the
  one-line notice:
  `Moved wendy credentials into the macOS Keychain (older wendy versions will need 'wendy auth login' again).`
- Any organic `Save()` (login, set-default, etc.) migrates as a side effect;
  the notice still prints only on the first migration (detected by "a field
  changed from inline to reference during this Save").

`WENDY_SECRET_STORE=file` disables dehydration: new saves write inline. That
is also the de-migration path — with the env set, a `Save()` resolves any
existing references and writes the values inline again (requires a working
Keychain read, i.e. a GUI session). Env semantics: `file` = inline-only
writes; unset = platform default (keychain on darwin, inline elsewhere);
`keychain` on a non-darwin platform has no backend and behaves like the
platform default (inline) — it never errors.

Keychain `Put` failure during dehydration must NOT lose the secret: the
field keeps its inline value and the save proceeds (plaintext, as today) —
worst case is the status quo, never a lost credential.

### 4. Lifecycle

- **Login/refresh** (`auth.go`) keep writing plaintext into the structs;
  `Save()` converts. No flow changes.
- **Logout/entry removal**: every path that deletes an `AuthConfig` or
  replaces a `CertificateInfo` deletes the referenced Keychain items
  (best-effort; a leaked item is inert without the config referencing it,
  but tidy-up is cheap). The implementation plan enumerates the exact sites
  (auth logout, cert refresh replacing entries, org removal).
- **Downgrade**: accepted per decision above. The migration notice names the
  consequence.
- The resumption ticket cache is unaffected (separate service name, separate
  env, references only ever appear in `config.json`).

### 5. Testing

Unit (faked `security` runner throughout):
- ref round-trip: Save dehydrates (field becomes `keychain:v1:<account>`,
  runner saw stdin-only write) → accessors resolve the original value.
- memoization: N accessor calls = 1 runner invocation.
- accessor failure: locked/denied runner → the actionable error text.
- `WENDY_SECRET_STORE=file`: no dehydration; existing refs resolved and
  inlined on Save (de-migration).
- Put-failure during Save → field stays inline, Save succeeds.
- non-darwin (`GOOS` build-tag selection): Save never dehydrates; config
  behavior byte-identical to today.
- logout/removal deletes the referenced items.

Integration: a login-shaped flow (`AddAuth` with plaintext → `Save` →
fresh `Load` → `PrivateKeyPEM()`/`BearerToken()` return the secrets, JSON on
disk contains no secret material).

Manual PR gate (real Mac): migrate a real config, confirm promptless reads
via the actual CLI binary, confirm `wendy device info` works end-to-end
against a provisioned device post-migration.

## Non-goals

- Secret-Service (Linux) / DPAPI (Windows) backends — interface leaves room.
- Encrypting the whole config file.
- Agent-side (device) key storage — different platform, different trust
  model.
- Removing the buildx registry-proxy temp key file (`docker.go` writes the
  key to a `0600` temp file for the proxy); it now sources the key via the
  accessor but the temp-file mechanism itself is out of scope.
