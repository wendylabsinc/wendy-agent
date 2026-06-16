# Registry pull + `wendy device apps install`

Status: Draft
Date: 2026-06-17
Branch: `jo/registry-pull-apps-install` (draft PR)

## Goal

Let wendy-agent pull container images directly from registries — Docker Hub,
the Wendy internal/cloud registry, or any private registry with credentials —
and add a `wendy device apps install` command that presents a curated catalog of
common apps (redis, postgres, homeassistant, …) merged with the org's own app
releases.

The device does the pull (not the laptop), so large public images never round-trip
through the developer's machine.

## Background

- The agent's containerd client already pulls non-local images over HTTPS via
  containerd's default docker resolver (`go/internal/agent/containerd/client.go`,
  the `GetImage` → `Pull` fallback). There is **no auth** wired into that resolver,
  so private/authenticated pulls fail today.
- `CreateContainer` (RPC in `wendy_agent_v1_container_service.proto`) already takes
  an `image_name` + `app_config` (the `wendy.json` bytes) and creates+configures a
  container from a locally-available-or-pulled image. Installing a catalog app is
  exactly "create a container from an image ref the device pulls" — no new RPC is
  required, only an auth field.
- The cloud already exposes `ListAppReleases`, `GetPullCredentials`, and
  `GetPushImageCredentials` (`~/git/wendy/cloud/proto/deployments.proto`).

## Design

### 1. Proto (`Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto`)

Add a `RegistryAuth` message and an optional field on `CreateContainerRequest`:

```protobuf
message RegistryAuth {
  string registry_host = 1; // e.g. "registry-1.docker.io", "ghcr.io"
  string username      = 2;
  string password      = 3; // password or token
}

message CreateContainerRequest {
  // ... existing fields ...
  optional RegistryAuth registry_auth = 9;
}
```

No new RPC. Regenerate Go bindings (`agentpb`).

#### Image PATH precedence (fix)

Running arbitrary registry images exposed a pre-existing agent bug: the base
env hardcoded `PATH=/usr/local/sbin:…:/bin` and appended it last, so per OCI
"last KEY wins" it clobbered any image's custom `PATH`. Images whose entrypoint
relies on a custom PATH (e.g. grafana, whose `/run.sh` does `exec grafana` with
the binary at `/usr/share/grafana/bin`) failed with `exec: <binary>: not found`
(exit 127). `mergeContainerEnv` now treats `PATH`/`TERM` as defaults the image
or request may override, while `WENDY_*` identity vars still win.

#### Catalog scope

`redis`, `postgres`, `mosquitto`, `homeassistant`, and `grafana` are
self-contained single containers. `paperless` was dropped: paperless-ngx hard
-requires a Redis broker (and ideally Postgres), so it cannot run as a single
container — it belongs in a future multi-service catalog entry that bundles its
dependencies.

### 2. Agent (`go/internal/agent/containerd/client.go`)

In the `Pull` fallback path:

- If `registry_auth` is present and the image is **not** the device-local registry,
  build a resolver with an authorizer scoped to `registry_host` using
  `docker.NewDockerAuthorizer(docker.WithAuthCreds(func(host string) (string, string, error)))`
  returning the supplied creds when `host` matches.
- No auth → current anonymous HTTPS pull (works for public Docker Hub images).
- Local-registry images keep the existing `PlainHTTP` path (no auth).
- Map pull failures to clear errors; surface 401/403 distinctly so the CLI can
  prompt for credentials.

The `CreateContainer`/`CreateContainerWithProgress` service handlers thread the new
field from the request into the containerd client.

### 3. CLI catalog package (`go/internal/cli/catalog/`)

- `catalog.json` embedded via `//go:embed`. Each entry:
  ```json
  {
    "name": "redis",
    "image": "docker.io/library/redis:7",
    "description": "In-memory key-value store",
    "defaultConfig": { /* wendy.json AppConfig: entitlements, ports, volumes */ }
  }
  ```
  Initial set: redis, postgres, homeassistant, mosquitto, grafana (extendable).
- `Load() ([]Entry, error)` parses+validates the embedded JSON; each entry's
  `defaultConfig` must produce a schema-valid `appconfig.AppConfig`.
- `Lookup(name string) (Entry, bool)` resolves a catalog name.
- Org apps are fetched separately by the command via the cloud client
  (`ListAppReleases`), not baked into this package.

### 4. CLI command (`go/internal/cli/commands/apps.go`)

`wendy device apps install [name|image]`:

- No arg + interactive terminal → a searchable picker of the curated catalog,
  grouped by **category** (Database, Home & IoT, Observability, Documents). The
  picker uses the existing `tui.Picker` with `Filterable` (find-as-you-type)
  enabled and a Category column; rows are grouped via `SortKey`.
- **Web UI**: catalog entries for apps with a web interface declare a
  `hooks.postStart.openURL` (e.g. `http://${WENDY_HOSTNAME}:3000`). After a
  successful install+start, the command expands the URL against the device host
  and opens it in the developer's browser, reusing the same `browseropen` +
  `expandHookEnv` mechanism as `wendy run`. Raw-image installs (no hook) don't
  open anything.
- Resolves the selection:
  - catalog name → catalog `image` + `defaultConfig`;
  - org release → internal-registry image ref (from `GetPullCredentials`/release
    metadata) + a minimal synthesized config;
  - otherwise treat the argument as a raw image ref with a minimal config.
- Synthesizes an `appconfig.AppConfig` (appId = `--name` or derived from the image),
  marshals to JSON, and calls `CreateContainer{image_name, app_config, registry_auth?}`
  followed by `StartContainer`.
- Auth resolution order:
  1. explicit `--username` / `--password` (or `--password-stdin`);
  2. `~/.docker/config.json` entry for the image's registry host;
  3. cloud `GetPullCredentials` for internal-registry refs (when logged in);
  4. anonymous.
- Agent-connected path only. Provider/BLE targets return
  "not supported on this device" (matches the other `apps` subcommands).
- Flags: `--username`, `--password`, `--password-stdin`, `--tag`/`--version`,
  `--name`, `--detach`.

## Data flow

```
wendy device apps install redis
  → catalog lookup: redis → docker.io/library/redis:7 + default wendy.json
  → resolve auth (none for public)
  → CreateContainer{image_name, app_config, registry_auth?}
  → agent: GetImage miss → Pull(image, WithAuthorizer) → unpack → create
  → StartContainer
```

## Error handling

- Unknown catalog name that is not a valid image ref → print available apps.
- 401/403 on pull → "registry authentication failed for <host>; pass
  --username/--password or run `wendy auth login`".
- Cloud unreachable → still show the embedded catalog; warn that org apps are
  unavailable.

## Testing

- **Agent**: table test that `registry_auth` yields an authorizer for the matching
  host and anonymous resolver otherwise; verify local-registry path stays PlainHTTP.
- **Catalog**: embedded JSON parses; every entry's `defaultConfig` is a schema-valid
  `AppConfig`; `Lookup` resolves known names and rejects unknown ones.
- **CLI**: catalog + org merge ordering; auth resolution order; image-ref vs
  catalog-name resolution; synthesized `AppConfig` is valid.
- **E2E (optional)**: install a public image onto a test agent and assert it runs,
  mirroring the existing chunk-diff e2e style.

## Status note (2026-06-17)

Internal/private-registry **installs work today** via an explicit image
reference plus credentials, e.g.
`wendy device apps install registry.example.com/team/app:1.2.3 --username … --password …`
(or `~/.docker/config.json`). The agent pulls the authenticated image directly.

The **convenience listing of org app releases in the picker is deferred**: the
`wendyos` copy of the cloud proto (`Proto/cloud/deployments.proto`) does not yet
expose `GetPullCredentials`, so an `AppRelease` (which carries only an image
digest, not a full registry reference) cannot be resolved to a pullable image
ref in-repo. Listing org apps without a working install would be a dead-end, so
the picker shows the curated catalog only until the cloud proto is synced. The
follow-up is: sync `GetPullCredentials` into `Proto/cloud`, then resolve a
selected release to `registry_url`/`full_artifact_path` + short-lived creds at
install time.

## Scope / YAGNI

- No new cloud APIs — reuse `ListAppReleases` + `GetPullCredentials` (the latter
  pending a proto sync; see status note).
- No persistent "installed-from-catalog" bookkeeping; installed apps are normal
  apps managed by existing `apps list/stop/remove`.
- Draft PR focuses on agent pull-with-auth, the catalog package, and the install
  command. E2E is optional for the draft.

## Out of scope

- Pushing to external registries from the CLI (separate concern, already partially
  covered by the cloud push-credentials path).
- A cloud-served catalog (embedded list is sufficient for the draft; revisit if the
  list needs to change without a CLI release).
