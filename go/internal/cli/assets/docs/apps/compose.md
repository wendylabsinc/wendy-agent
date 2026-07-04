# Multi-Service Apps with Docker Compose

Wendy supports running multi-service applications defined in a standard `docker-compose.yml` (or `compose.yml`) file. This is the recommended approach when your app needs more than one container — for example, an API service alongside a database, or a perception pipeline with several processing stages.

## How it works

When you run `wendy run` in a directory that contains a compose file but no `wendy.json`, Wendy automatically detects it as a compose project. Each service is built, pushed to the device's embedded container registry, and started in dependency order.

Supported compose file names (checked in order):
- `docker-compose.yml`
- `docker-compose.yaml`
- `compose.yml`
- `compose.yaml`

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

Wendy selects a device, builds each service image, pushes them to the device, and starts them concurrently with interleaved log output. Each service's lines are prefixed with a color-coded, column-aligned service name. Colors rotate through cyan, yellow, green, magenta, blue, and red. Log lines are never interleaved mid-line — each line is written atomically:

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
| `depends_on` | Dependency order: list or condition-map form. Services are created in dependency order; detached starts follow the same order, but health checks and readiness conditions are not evaluated. |
| `restart` | Restart policy: `no`, `on-failure`, `always`, `unless-stopped`. Overridden by CLI flags if specified. |

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

# Named volumes must be declared at the top level
volumes:
  shared-data:
```

> Host bind mounts (e.g. `./local-path:/container/path`) are not supported on device; they are skipped.

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
- Service-specific lifecycle behavior is not supported yet: Compose services cannot declare Wendy readiness probes or `postStart` hooks, and top-level `wendy.json` hooks do not apply to generated Compose service apps.
- Host networking does not imply shared IPC or shared `/dev/shm`; ROS 2 shared-memory transport requires an app shape that can explicitly share namespaces.
- Linux containers on macOS require a target WendyOS device; local Docker Desktop compose is used as a fallback when no device is targeted.
- Compose `extends`, `profiles`, and `secrets` are not supported.
