# Multi-Service Apps with Docker Compose

Wendy supports running multi-service applications defined in a standard `docker-compose.yml` (or `compose.yml`) file. This is the recommended approach when your app needs more than one container, for example an API service alongside a database, or a perception pipeline with several processing stages.

## How it works

When you run `wendy run` in a directory that contains a compose file, Wendy automatically detects it as a compose project. Each service is built, pushed to the device's embedded container registry, and started in dependency order. No `wendy.json` is required; each service carries its own configuration, and an optional companion `wendy.json` in the same directory is merged in when present (see [Readiness probes and postStart hooks](#readiness-probes-and-poststart-hooks)).

Supported compose file names (checked in order, first found wins):
- `docker-compose.yml`
- `docker-compose.yaml`
- `compose.yml`
- `compose.yaml`

A compose file takes precedence over a `Dockerfile` in the same directory. To force the single-container Docker path instead, pass `--build-type docker` or `--dockerfile <path>`.

## Quickstart

```
my-project/
  docker-compose.yml
  api/
    Dockerfile
    main.py
  worker/
    Dockerfile
    worker.py
```

```yaml
# docker-compose.yml
services:
  api:
    build: ./api
    network_mode: host

  worker:
    build: ./worker
    network_mode: host
    depends_on:
      - api
```

```sh
wendy run
```

Wendy selects a device, builds each service image, pushes them to the device, and starts them concurrently with interleaved log output. Each service's lines are prefixed with a color-coded, column-aligned service name. Colors rotate through cyan, yellow, green, magenta, blue, and red. Log lines are never interleaved mid-line; each line is written atomically:

```
[api]     Listening on :8080
[worker]  Connected to api
[worker]  Processing item 1
```

Press **Ctrl-C** to stop all services. Services are stopped in reverse dependency order; the CLI prints `Stopping <name>...` for each service and then a final `Stopped N service(s).` summary.

## Detached mode hint

When all services start in detached mode (`--detach`), the CLI prints a hint showing how to stream logs:

```
Run 'wendy device logs' to stream logs.
```

## Service fields

Wendy honours the following compose fields. Fields not listed here are ignored.

| Field | Description |
|-------|-------------|
| `build` | Build context: a path string or a `{ context, dockerfile, args }` mapping. Custom Dockerfile paths must resolve inside the build context. Services without `build` use a pre-built image via `image`. |
| `image` | Pre-built image to pull and run on the device (e.g. `redis:7-alpine`). Public image names are normalised to their fully-qualified form automatically. |
| `command` | Override the container's default command. Accepts a string (shell-split) or a YAML sequence. |
| `environment` | Environment variables to inject. Parsed from key-value maps or `KEY=VALUE` lists. Applied in order: image env → compose env → Wendy system vars → framework vars (e.g., ROS2) → OTEL vars. OCI last-wins semantics apply. |
| `ports` | Port mappings (`host:container`). Adds a `network` entitlement when present. |
| `network_mode: host` | Adds a `host` network entitlement. |
| `volumes` | Named volumes are created as `persist` entitlements. Host bind mounts (paths starting with `.` or `/`) are silently skipped. |
| `depends_on` | Dependency order: list or condition-map form. Services are created in dependency order; detached starts follow the same order, but the condition-map's own health-check conditions (e.g. `condition: service_healthy`) are not evaluated; ordering is purely topological. To gate a service's own postStart hook on its readiness instead, see [Readiness probes and postStart hooks](#readiness-probes-and-poststart-hooks). |
| `restart` | Restart policy: `no`, `on-failure`, `always`, `unless-stopped`. Overridden by CLI flags if specified. |
| `x-wendy` | Wendy-specific per-service extensions: `readiness` (a readiness probe) and `hooks` (postStart hooks). Same camelCase schema as `wendy.json`'s top-level `readiness`/`hooks` fields. See [Readiness probes and postStart hooks](#readiness-probes-and-poststart-hooks) below. |

The following fields are recognized but intentionally ignored, each with a warning at deploy time: `devices`, `privileged`, `cap_add`, `security_opt`, `ipc`, `pid`, `shm_size`, `healthcheck`, `profiles`, `secrets`, and `extra_hosts`. Hardware access goes through Wendy entitlements in a companion `wendy.json` instead. Anything else (such as `networks`, `deploy`, `entrypoint`, `env_file`, or long-form volume syntax) is ignored silently.

## Networking

Services communicate over the host network by default when `network_mode: host` is set. This is the simplest option for robotics and edge workloads where services need to share ports or use multicast.

For isolated networking, omit `network_mode` and use `ports` mappings. Each service gets its own network namespace. Services must reach each other over host-exposed ports.

## Volumes

Named volumes declared in `volumes:` become persistent storage on the device and survive container restarts and re-deployments. Two services sharing a volume name share the same storage.

```yaml
services:
  producer:
    build: ./producer
    volumes:
      - shared-data:/data/out

  consumer:
    build: ./consumer
    volumes:
      - shared-data:/data/in

# Declare named volumes at the top level so the file stays valid for
# plain `docker compose` too. Wendy itself only reads the `services:`
# block; the service-level references above are what create the storage.
volumes:
  shared-data:
```

> Host bind mounts (e.g. `./local-path:/container/path`) are not supported on device; they are skipped.

## Readiness probes and postStart hooks

A compose service can declare its own readiness probe and postStart hooks under `x-wendy`, using the same camelCase schema as `wendy.json`'s `readiness`/`hooks` fields:

```yaml
services:
  frontend:
    build: ./frontend
    ports:
      - "3000:3000"
    x-wendy:
      readiness:
        tcpSocket:
          port: 3000
        timeoutSeconds: 30
      hooks:
        postStart:
          openURL: "http://${WENDY_HOSTNAME}:3000"
```

`readiness` here gates only `frontend`'s own `postStart` hook; it never delays any other service's startup (`depends_on` ordering is unaffected). `hooks.postStart.agent` (a command run on the device) is attached only to the declaring service's own container.

A companion `wendy.json` in the same directory can also declare `services.<name>.readiness` / `services.<name>.hooks` for a service. When both are present, the companion wins wholesale per field: its `readiness` and `hooks` each replace the `x-wendy` value entirely rather than merging with it.

A companion `wendy.json`'s *top-level* `readiness`/`hooks` act instead as an app-level fallback: they fire once after every service in the project has started, rather than gating any single service. Both the fallback and a service's own `x-wendy` hooks fire if both are declared. [Examples/WendyMC](../../Examples/WendyMC) is a live example of this: its companion `wendy.json` declares a top-level `readiness.tcpSocket` (port 8080) and `hooks.postStart.openURL`, so `wendy run` waits for the web UI to come up and opens it automatically once both of its services have started. A top-level `hooks.postStart.agent` is the one exception: a compose app has no app-level container to run an agent-side hook in, so it is ignored (with a warning); declare an agent-side hook under a service's `x-wendy.hooks` or the companion's `services.<name>.hooks` instead.

In attached mode, each service's readiness→postStart sequence runs asynchronously right after that service's start is acknowledged, so a slow or failing probe never delays starting the next service; Ctrl-C cancels any in-flight readiness wait and kills `cli` hook child processes. In detached mode, readiness is waited sequentially in dependency order after every service has started; hooks outlive the CLI once it exits, and a readiness failure only prints a warning; it never fails the command.

Hook commands may reference `${WENDY_HOSTNAME}` (the device host), `${WENDY_APP_ID}`, and `${WENDY_SERVICE_NAME}` (the declaring service's name; empty for the app-level fallback). Windows-style `%VAR%` forms are accepted too.

A note on naming for single-service projects: a compose file with more than one service groups its containers under the project name (`WENDY_APP_ID` = the project directory name, `WENDY_SERVICE_NAME` = the service name). A **single-service** compose project *without a companion `wendy.json`* instead keeps the legacy `<project>-<service>` app ID for backward compatibility, so `WENDY_SERVICE_NAME` expands to the empty string and `WENDY_APP_ID` is e.g. `myproj-web` (not `myproj`). A companion's `appId` and grouped naming always take over, even for a single service. Write hook commands accordingly if your project has only one service and no companion.

> Readiness probes dial the device host, not the container directly, so a service's probed port must be published (`ports:`) or the service must use `network_mode: host` for the probe to succeed.

## Flags

All `wendy run` flags work with compose projects:

| Flag | Description |
|------|-------------|
| `--deploy` | Build and create all containers but do not start them. |
| `--detach` | Start all containers but do not stream logs. |
| `--restart-unless-stopped` | Set restart policy to `unless-stopped` for all services (overrides per-service setting). |
| `--restart-on-failure` | Set restart policy to `on-failure` for all services. |
| `--no-restart` | Disable restart for all services. |
| `--debug` | Enable debug logging during build and run. |
| `--yes` / `-y` | Accept all device-selection prompts automatically. |

## Limitations

- Headless Mac is not supported. `wendy run` rejects compose projects before any registry or Docker setup when the selected target is Headless Mac. Target a Linux/WendyOS device to use compose.
- Wendy-specific hardware access entitlements such as `gpu`, `display`, `camera`, `audio`, `bluetooth`, `usb`, `i2c`, `gpio`, `spi`, `input`, and `serial` are not inferred from compose fields.
- Host networking does not imply shared IPC or shared `/dev/shm`; ROS 2 shared-memory transport requires an app shape that can explicitly share namespaces.
- Linux containers on macOS require a target WendyOS device; local Docker Desktop compose is used as a fallback when no device is targeted.
- Compose `extends`, `profiles`, and `secrets` are not supported.
- `wendy fleet run` does not support compose projects yet; deploy to one device at a time.
- `wendy run --service` applies only to `wendy.json` `services` maps; the compose path always deploys every service.
- `wendy build` for a compose project shells out to `docker compose build`. The Apple Container builder is not supported for compose; use `--builder docker`.
