# HelloMultiService

A minimal two-service Wendy app that demonstrates the multi-service container
naming convention introduced in WDY-878.

## Structure

```
HelloMultiService/
├── api/        ← FastAPI backend  (serviceName: "api")
└── worker/     ← background worker (serviceName: "worker")
```

Both services share the same `appId` (`sh.wendy.examples.multiservice`) but
each has its own `wendy.json` with a distinct `serviceName`.

## How services are named

| | `api` service | `worker` service |
|---|---|---|
| Container name | `sh.wendy.examples.multiservice/api` | `sh.wendy.examples.multiservice/worker` |
| `WENDY_HOSTNAME` | `api.local` | `worker.local` |
| `WENDY_APP_GROUP` | `sh.wendy.examples.multiservice` | `sh.wendy.examples.multiservice` |
| `WENDY_APP_ID` | `sh.wendy.examples.multiservice` | `sh.wendy.examples.multiservice` |
| `WENDY_DEVICE_HOSTNAME` | `<device>.local` | `<device>.local` |

The worker reaches the API at `http://api.local:8000` — the sibling service's
mDNS hostname, which is always `{serviceName}.local`.

`WENDY_DEVICE_HOSTNAME` is the host device's mDNS name and is the same for
every service in the app.

## Running

Deploy each service from its own directory:

```bash
cd Examples/HelloMultiService/api
wendy run

cd Examples/HelloMultiService/worker
wendy run
```

Both `wendy run` commands must use the same device so the services share a
network namespace and can resolve each other's mDNS hostnames.

## What to observe

Once both services are running, the worker logs show:

```
=== HelloMultiService worker started ===
  WENDY_HOSTNAME        = worker.local
  WENDY_APP_GROUP       = sh.wendy.examples.multiservice
  WENDY_APP_ID          = sh.wendy.examples.multiservice
  WENDY_DEVICE_HOSTNAME = wendyos-<name>.local
  API endpoint          = http://api.local:8000

API is up at http://api.local:8000
[worker] submitted demo job
[worker] processing job 0: 'hello from worker'
[worker] no pending jobs
```

The API `GET /` response shows its own identity:

```json
{
  "service": "api",
  "wendy_hostname": "api.local",
  "wendy_app_group": "sh.wendy.examples.multiservice",
  "wendy_app_id": "sh.wendy.examples.multiservice",
  "wendy_device_hostname": "wendyos-<name>.local",
  "note": "Worker reaches this service at http://api.local:8000"
}
```
