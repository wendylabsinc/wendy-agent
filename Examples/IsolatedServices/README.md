# IsolatedServices

Demonstrates `isolation: isolated` — a multi-service app where each container
gets its own CNI-assigned IP, and sibling service names resolve via an
agent-managed `/etc/hosts` file (WDY-887, WDY-888, PR #897).

```
IsolatedServices/
├── wendy.json   ← isolation: isolated, two services with dependsOn
├── api/         ← HTTP server; reports its CNI IP
└── worker/      ← resolves "api" via /etc/hosts and calls http://api:8080
```

## How isolated networking works

With `isolation: isolated` Wendy (PR #897):

1. **CNI bridge** — after each container starts, the agent runs `bridge` CNI
   plugin to assign a deterministic `/28` subnet IP from the `10.0.0.0/8`
   range.  The subnet is derived from a SHA-256 of the `appId`, so the same
   app always gets the same block.
2. **`/etc/hosts` bind-mount** — a read-only `/etc/hosts` file containing
   `<ip>\t<service-name>` entries for every sibling is bind-mounted into each
   container.  It is updated atomically after each successful CNI ADD, so
   later-starting containers see the IPs of already-running siblings.

This means services can call each other by **service name** (`http://api:8080`,
`http://worker:9090`, …) without a sidecar DNS server or host networking.

## wendy.json

```jsonc
{
  "appId": "sh.wendy.examples.isolatedservices",
  "platform": "linux",
  "isolation": "isolated",
  "services": {
    "api": {
      "context": "./api"
    },
    "worker": {
      "context": "./worker",
      "dependsOn": ["api"]    // api starts first (topo order)
    }
  }
}
```

## Run

```sh
cd Examples/IsolatedServices
wendy run
```

Expected output:

```
[api] CNI-assigned IP: 10.x.y.2
[api] listening on :8080
[worker] CNI-assigned IP: 10.x.y.3
[worker] WENDY_HOSTNAME = worker.local
[worker] resolving 'api' via /etc/hosts ...
[worker] api → 10.x.y.2
[worker] calling http://api:8080 ...
[worker] api response:
{
  "service": "api",
  "ip": "10.x.y.2",
  "wendy_app_id": "sh.wendy.examples.isolatedservices",
  ...
}
[worker] ✓ service name resolution via /etc/hosts works
```

## Dependency ordering (WDY-879)

`dependsOn: ["api"]` tells Wendy to start `api` before `worker`.  On stop,
Wendy reverses the order (stop dependents first) so teardown is clean.

`wendy device ps` (new alias for `apps list`, WDY-894) shows both containers:

```sh
wendy device ps
```

## See also

- [SharedIPC](../SharedIPC) — shared-ipc isolation (shared namespaces + /dev/shm)
- [HelloMultiService](../HelloMultiService) — per-service wendy.json pattern
