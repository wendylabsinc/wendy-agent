# Multi-Service Apps with `wendy.json`

When your project needs more than one container managed through a `wendy.json` file (rather than a `docker-compose.yml`), declare a `services` map in `wendy.json`. `wendy run` detects the map and automatically orchestrates a parallel multi-service build and deployment.

## `wendy.json` structure

```json
{
  "appId": "com.example.myapp",
  "services": {
    "db": {
      "context": "db"
    },
    "api": {
      "context": "api",
      "dependsOn": ["db"],
      "entitlements": [
        { "type": "network", "mode": "host" }
      ]
    },
    "frontend": {
      "context": "frontend",
      "dependsOn": ["api"]
    }
  }
}
```

### `services` map

Each key is a service name. Each value is a `ServiceConfig` object:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `context` | string | **yes** | Build context directory, relative to `wendy.json`. Must be a relative path and must not contain `..` components. |
| `entitlements` | array | no | [Entitlements](../wendy-agent/oci/entitlements.md) to apply to this service's container. Same schema as the top-level `entitlements` field. |
| `dependsOn` | array of strings | no | Names of other services in this `services` map that must be created before this one. All referenced names must exist in the same map. |

### Validation rules

`wendy.json` validation rejects the following:
- A service with an empty or missing `context`.
- A `context` that is an absolute path.
- A `context` that contains `..` path components.
- A `dependsOn` entry that references a service name not present in the `services` map.
- A service `entitlements` entry with an unknown or missing type.
- A service `persist` entitlement missing `name` or `path`, or with a non-absolute or `..`-containing `path`.
- A service `network` entitlement with a `mode` other than `"host"` or `"none"`.
- A service `i2c` entitlement with a device not in `i2c-N` format.
- A service `serial` entitlement with a device not matching the USB-only `ttyACM0` / `ttyUSB0` (`tty*N`) pattern.
- A service `mcp` entitlement with a port outside the range 1–65535.
- More than one `mcp` entitlement within a single service's `entitlements` array.

`ValidateJSON` additionally warns on deprecated entitlement types and unknown entitlement keys within service-level `entitlements` arrays, using the same rules applied to the top-level `entitlements` field.

## How `wendy run` handles multi-service projects

When `appCfg.Services` is non-empty, `wendy run` routes to the multi-service pipeline:

1. **Parallel build** — all service images are built and pushed concurrently, with a maximum of 4 simultaneous builds. In interactive terminals a per-service spinner displays each service's status (`waiting` → `building…` → `built (Xs)` / `failed`). In non-interactive terminals plain log lines are printed instead.
2. **Ordered container creation** — containers are created one at a time in topological dependency order. A service listed in another service's `dependsOn` is created first.
3. **Start and stream** — all containers are started and their combined stdout/stderr is multiplexed to the terminal. Each line is prefixed with `[serviceName]`.

Press **Ctrl-C** to stop all services. The CLI cancels all streams, issues a `StopContainer` for each service concurrently, and waits up to 30 seconds before exiting.

### Container naming

Each service container ID follows the `{appId}/{serviceName}` convention. For
example, with `appId: "com.example.myapp"` and service `"api"`, the containerd
container ID is `com.example.myapp/api`. The corresponding snapshot key uses
`@` as the separator (`wendy-com.example.myapp@api`) to remain unambiguous when
either component contains a hyphen. The cgroup path component uses `@` as the
separator: `system.slice:edge-agent:com.example.myapp@api` (the systemd service segment
reflects the `WENDY_SYSTEMD_SERVICE_NAME` env var, which defaults to `edge-agent`;
`@` is used because it cannot appear in either a valid appId or serviceName,
eliminating any collision risk from the hyphen separator).

> **Note:** Single-container apps (no `serviceName` in the top-level
> `wendy.json`) are unaffected — their container ID remains the bare `appId`.

## Filtering with `--service`

To build and run only a specific service (and its transitive dependencies):

```sh
wendy run --service api
```

This resolves `api` and all services reachable through its `dependsOn` graph. Services outside this subset are not built or started. Passing an unknown service name returns an error immediately.

## Flags

All standard `wendy run` flags apply. The following are particularly relevant for multi-service projects:

| Flag | Description |
|------|-------------|
| `--service <name>` | Build and run only the named service and its transitive `dependsOn` dependencies. |
| `--deploy` | Build and create all containers but do not start them. |
| `--detach` | Start all containers but do not stream logs. |

## Example layout

```
my-project/
  wendy.json
  db/
    Dockerfile
  api/
    Dockerfile
  frontend/
    Dockerfile
```

```sh
wendy run            # builds and starts all three services
wendy run --service api   # builds db and api only (frontend excluded)
```

## Limitations

- Log output is multiplexed with a `[serviceName]` prefix on each line. Per-service log stream routing is not yet available.
- Containers are created via individual `CreateContainer` calls in dependency order. A grouped `CreateAppGroup` RPC for atomic creation is planned as a follow-up.
- Wendy for Mac is not supported. `wendy run` rejects multi-service `wendy.json` projects when the selected target is Wendy for Mac, before any build or registry operation. Target a Linux/WendyOS device for multi-service workloads.
