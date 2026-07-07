# Multi-container AppStore install — design

**Date:** 2026-07-07
**Status:** Approved (design), pending implementation plan
**Repos touched:** `cloud` (Swift — manifest storage + API), `wendyos/go` (CLI — install flow)

## Problem

`wendy device install <app-id>` resolves an AppStore app to a **single** OCI image
(`GET /v1/apps/{id}/image`) and creates **one** container. Real self-hosted apps are
multi-service:

- **paperless-ngx** — webserver + Redis broker (DB defaults to SQLite in the data volume).
- **immich** — `immich-server` + `immich-machine-learning` + Redis + Postgres (with the
  vector extension).

Installing only the primary image leaves these apps non-functional. We need the AppStore
to describe an app as a set of services and the CLI to bring them all up, networked
together, with sensible config out of the box.

## Goals / non-goals

**Goals (v1)**
- AppStore can describe an app as an ordered set of services with images, env, ports,
  volumes, and inter-service dependencies.
- `wendy device install <app-id>` brings up all services on the device, networked so
  services reach each other by name, with **zero required flags** — self-contained,
  works out of the box.
- Ship two curated manifests: `paperless` (2 services) and `immich` (4 services).

**Non-goals (explicit follow-ups)**
- Install-time `--env` / `--volume` overrides.
- Healthchecks, per-service resource limits, a `wendy app config` command.
- Durable secret storage across reinstall (v1 treats reinstall as a fresh start).

## Key enabling facts (existing infrastructure)

Confirmed present in `wendyos/go`; the design reuses these rather than reinventing:

- **Container-to-container networking already exists.** In `isolation: "isolated"` mode the
  agent sets up a per-app CNI bridge (`internal/agent/containerd/cni.go`) and writes an
  `/etc/hosts` mapping `serviceName → IP` (`cni.go:writeHostsFile`, injected via
  `localoci.InjectHostsMount`, `client.go:859,1190`). A `server` container can reach
  `postgres`/`redis` **by service name** with no extra config.
- **Multi-service orchestration already exists.** `runMultiServiceWithAgent`
  (`internal/cli/commands/multibuild.go:257`) builds one `CreateContainerRequest` per
  service, orders by dependency (`appconfig/toposort.go:ServiceTopoOrder`), and
  creates+starts via `startAndStreamServices` (`multibuild.go:725`). Per-service config is
  assembled by `multiServiceCreateConfig` (`multibuild.go:692`).
- **Pre-built-image → container translation already exists.** The compose path
  (`compose.go:composeAppConfig`, `:327`) turns a service with a pre-built `image:` into an
  `appconfig.AppConfig`: ports → `Network` entitlement with `PortMapping`, named volumes →
  `Persist` entitlements, env via `Env`. This is the closest analog to what the install
  flow needs.
- **Agent transport for all of the above** is the serialized `appconfig.AppConfig` carried
  in `CreateContainerRequest.AppConfig` plus `Env []string`
  (`proto/.../container_service`); ports/volumes/networks are expressed as entitlements +
  isolation, not as dedicated proto fields. Dependency ordering is enforced **client-side**
  by call sequencing (the agent never sees the graph).

## Architecture

### 1. Cloud — manifest storage + API

- **New table** `app_manifests(app_id TEXT PRIMARY KEY, manifest JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`. One curated JSON document per
  multi-service app.
- **New endpoint** `GET /v1/apps/{app_id}/manifest`:
  - If an `app_manifests` row exists → return it verbatim.
  - Else → **synthesize a one-service manifest** from the existing `app_images` row (reusing
    `resolveAppImageReference`), so every app has a uniform manifest shape and the CLI has a
    single code path.
  - 404 only when the app id is in neither table.
- The existing `GET /v1/apps/{id}/image` is **unchanged** (back-compat for the already-shipped
  PR #1229 CLI).
- Follows the hand-rolled NIO handler style in `VideoHTTPHandler.swift` (same file as
  `handleAppImage`).

### 2. Manifest schema (structured JSON)

```json
{
  "app_id": "immich",
  "secrets": ["db_password"],
  "services": [
    { "name": "postgres",
      "image": "ghcr.io/immich-app/postgres:14-vectorchord0.3.0",
      "env": { "POSTGRES_PASSWORD": "${secret:db_password}",
               "POSTGRES_USER": "immich", "POSTGRES_DB": "immich" },
      "volumes": [ { "name": "pgdata", "path": "/var/lib/postgresql/data" } ],
      "ports": [], "dependsOn": [] },

    { "name": "redis", "image": "docker.io/redis:7", "ports": [], "dependsOn": [] },

    { "name": "machine-learning",
      "image": "ghcr.io/immich-app/immich-machine-learning:release",
      "volumes": [ { "name": "model-cache", "path": "/cache" } ],
      "ports": [], "dependsOn": [] },

    { "name": "server",
      "image": "ghcr.io/immich-app/immich-server:release",
      "env": { "DB_HOSTNAME": "postgres", "DB_USERNAME": "immich",
               "DB_DATABASE_NAME": "immich", "DB_PASSWORD": "${secret:db_password}",
               "REDIS_HOSTNAME": "redis",
               "IMMICH_MACHINE_LEARNING_URL": "http://machine-learning:3003" },
      "volumes": [ { "name": "upload", "path": "/usr/src/app/upload" } ],
      "ports": [ { "host": 2283, "container": 2283, "proto": "tcp" } ],
      "dependsOn": [ "postgres", "redis", "machine-learning" ] }
  ]
}
```

Field semantics:

- **`services[]`** — each has `name` (DNS name on the bridge, container name suffix),
  `image` (full OCI reference; curated manifests write the resolved reference directly),
  optional `env` (map), `volumes` (`{name, path}`), `ports` (`{host, container, proto}`),
  `dependsOn` (list of service names).
- **Cross-service addressing = literal service names.** Because the isolated-mode bridge
  publishes `/etc/hosts` entries, env values just use `postgres` / `redis` / a
  `http://machine-learning:3003` URL. No templating for hostnames.
- **Secrets.** `secrets` lists names; any `${secret:NAME}` occurrence in any service's env is
  replaced at install time with a single per-install random value. The same `NAME` resolves
  to the same value across services (so `postgres` and `server` share `db_password`).
- **Ports.** Only services that must be reachable from outside the device declare `ports`
  (→ host `Network` entitlement / `PortMapping`). Internal services (redis/postgres/ML)
  declare none and stay reachable only on the bridge.

### 3. CLI — install flow (`internal/cli/commands/apps_install.go`)

1. `resolveAppManifest(base, appID)` → `GET /v1/apps/{id}/manifest`, decode into a
   `manifest{ AppID, Secrets []string, Services []manifestService }`.
2. **Generate secrets once** (`crypto/rand`, hex/base64), one value per `secrets` entry.
   Substitute `${secret:NAME}` in every service's env.
3. For each service, synthesize an `appconfig.AppConfig`:
   - `AppID = appID`, `ServiceName = svc.name`, `Isolation = "isolated"`,
     `Services = <all service names>` (so the agent knows the group and writes the hosts file).
   - `Entitlements`: a `Network` entitlement carrying `svc.ports` as `PortMapping`s (omitted
     when empty), one `Persist` entitlement per volume (`Name`, `Path`).
   - `Env`: `KEY=VALUE` from the (secret-substituted) env map.
   This mirrors `composeAppConfig` / `multiServiceCreateConfig` — factor a shared helper if
   the mapping is identical.
4. **Topo-sort** by `dependsOn` (reuse `ServiceTopoOrder`); on a cycle, fail with a clear
   error.
5. **Create + start** each service in order using the existing
   `createContainerWithProgress` + `StartContainer` calls (reuse the `startAndStreamServices`
   pattern, including the create/start interleaving that shared-namespace groups need — here
   we use `isolated`, so straightforward ordered create-then-start).
6. A **single-service** manifest (the synthesized case) flows through the same path with one
   service — behavior identical to today's single-container install.

### 4. Secrets persistence (known v1 limitation)

Generated secrets are baked into each container's `Env` and persist with the container
across restarts (containerd stores the container spec). **Reinstalling regenerates the
secrets**, which will mismatch the data already in the named volumes (e.g. the Postgres
password). v1 documents reinstall as a fresh start; durable secret storage (a device-side
per-app secret store reused on reinstall) is a follow-up.

## Data flow

```
wendy device install immich
  → CLI GET /v1/apps/immich/manifest         (cloud: app_manifests row)
  → CLI generates db_password, substitutes ${secret:db_password}
  → topo order: postgres, redis, machine-learning, server
  → for each: CreateContainer(AppConfig{isolation:isolated, service, ports, volumes, env})
              + StartContainer
  → agent: per-app CNI bridge + /etc/hosts (postgres/redis/machine-learning/server → IPs)
  → server reaches postgres/redis by name; host port 2283 → device IP
```

## Error handling

- Manifest 404 → "app not in the AppStore" (same message as today's `/image` 404).
- Dependency cycle in `dependsOn` → fail before creating any container.
- A service create/start failure → surface the failing service name; leave already-started
  services running (do not roll back in v1) and report which came up. (Matches the existing
  multi-service "deploy what succeeds" posture; revisit if partial installs prove confusing.)
- Unknown `${secret:NAME}` with no matching `secrets` entry → fail fast (manifest bug).

## Testing

- **Cloud:** unit-test the manifest endpoint for (a) an explicit `app_manifests` row and
  (b) a synthesized single-service manifest from `app_images`; migration up/down round-trip
  against Postgres. Seed the `paperless` and `immich` manifests via migration and assert they
  parse.
- **CLI:** unit-test manifest → `AppConfig` mapping, `${secret:}` substitution (shared name →
  shared value; missing name → error), and topo ordering (incl. cycle detection). Table-driven
  against the two curated manifests.
- **E2E:** install `paperless` on a device; assert both containers running, the web port
  reachable on the device IP, and a restart preserves data.

## v1 scope

- Cloud: `app_manifests` table + migration, `/manifest` endpoint, seeded `paperless` +
  `immich` manifests.
- CLI: manifest resolution, secret substitution, per-service `AppConfig` synthesis, ordered
  create/start reusing existing helpers.
- Curated manifests: `paperless` (webserver + redis, SQLite DB in the data volume) and
  `immich` (server + machine-learning + redis + postgres).

## Open questions deferred to the plan

- Exact curated env for `immich` (mirror the upstream `docker-compose.yml` / `.env` defaults;
  pin image tags to a known-good release rather than floating `:release`).
- Whether to factor the compose and manifest → `AppConfig` mapping into one shared helper now
  or after the CLI code lands.
