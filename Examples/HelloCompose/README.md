# HelloCompose

A two-service compose example that demonstrates the **companion `wendy.json`** pattern.

```
HelloCompose/
├── docker-compose.yml   ← service topology (fully Docker Desktop-compatible)
├── wendy.json           ← Wendy-specific config: appId + hardware entitlements
├── api/                 ← HTTP API with GPU entitlement
└── client/              ← polls the API, prints identity info, exits
```

## The companion pattern

`docker-compose.yml` defines service topology — images, networking, dependencies.
It stays **unmodified and fully compatible with Docker Desktop**.

`wendy.json` sits alongside it and provides everything Wendy-specific:

```jsonc
// wendy.json
{
  "appId": "sh.wendy.examples.hellocompose",
  "platform": "linux",
  "services": {
    "api": {
      // GPU access declared here, not in docker-compose.yml
      "entitlements": [{ "type": "gpu" }]
    }
  }
}
```

When you run `wendy run`, the CLI merges both files:
- Service topology, networking, and `depends_on` come from `docker-compose.yml`
- `appId`, entitlements, and runtime config come from `wendy.json`
- Services in compose but absent from `wendy.json` get only the synthesised entitlements (ports → network, named volumes → persist)
- If a service name in `wendy.json` has no match in the compose file, the CLI warns

## Run on a Wendy device

```sh
cd Examples/HelloCompose
wendy run
```

Expected output:

```
[api]    Starting on :8080  (Python 3.11.x, arm64)
[api]    GPU: NVIDIA Orin  (or "none detected" if run on CPU-only hardware)
[client] WENDY_APP_ID          = sh.wendy.examples.hellocompose
[client] WENDY_HOSTNAME        = client.local
[client] WENDY_DEVICE_HOSTNAME = wendyos-<name>.local
[client] Waiting for api at http://wendyos-<name>.local:8080 ...
[client] Hello from the API!
[client] API machine: arm64  python: 3.11.x
[client] API GPU:     NVIDIA Orin
[client] API WENDY_APP_ID:    sh.wendy.examples.hellocompose
[client] API WENDY_HOSTNAME:  api.local
[client] Note: GPU entitlement is declared in wendy.json; docker-compose.yml stays Docker Desktop-compatible.
[client] Done.
```

## Run locally with Docker Desktop

`docker-compose.yml` contains no Wendy-specific extensions, so it works as-is:

```sh
docker compose up
```

The client falls back to `http://api:8080` when `WENDY_DEVICE_HOSTNAME` is
absent, using Docker's built-in service-name DNS. Wendy env vars
(`WENDY_APP_ID`, `WENDY_HOSTNAME`, …) will show `"not set"`.

## Service identity

Both services share the same `appId` (from `wendy.json`). The Wendy agent injects:

| Variable | `api` service | `client` service |
|---|---|---|
| `WENDY_APP_ID` | `sh.wendy.examples.hellocompose` | `sh.wendy.examples.hellocompose` |
| `WENDY_HOSTNAME` | `api.local` | `client.local` |
| `WENDY_DEVICE_HOSTNAME` | `<device>.local` | `<device>.local` |
