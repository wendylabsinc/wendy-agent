Wendy devices produce their structured logs, metrics and tracing through [OpenTelemetry](../wendy-agent/otel.md).

These logs are received by a local on-device collector, which forwards the stream to other targets. One of those targets is the Wendy Cloud.

> **NOTE (nit)**: This **should** re-use an [existing gRPC connection](connectivity.md) between Wendy-Agent and Wendy Cloud.

The OTel spec is a standard wire protocol, also based on gRPC, that we can implement and codegen. Wendy Cloud then collects the OTel data, and "processes" it. Any queries over structured logs, visualisation and forwarding of data happens on these streams.

## Environment variables injected into app containers

When the agent starts an app container it injects a set of environment variables
on top of the image's own env. Two are always present (all network modes):

| Variable | Value |
|---|---|
| `WENDY_HOSTNAME` | For single-container apps: the device's mDNS hostname (omitted when unresolvable). For multi-service apps (`serviceName` set): `{serviceName}.local`, giving each service a distinct hostname identity. |
| `WENDY_APP_ID` | The `appId` from `wendy.json` (omitted when empty). |
| `WENDY_APP_GROUP` | The `appId` of the owning app. **Multi-service only** — injected when `serviceName` is non-empty so a service can discover its siblings. Absent for single-container apps. |

The OpenTelemetry variables are injected **only when the app has the host
`network` entitlement**, because the agent's local OTLP receiver listens on the
device loopback and is reachable only from a host-networked container. Each is
left untouched if the image already sets it, so image-provided values win:

| Variable | Value | Notes |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://127.0.0.1:4317` (port from `WENDY_OTEL_PORT`, default `4317`) | Only set if the image hasn't configured an endpoint. |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` | Only set together with the default endpoint above (i.e. when the image hasn't set an endpoint) and only if absent. An image that sets its own endpoint keeps full control of the protocol. |
| `OTEL_SERVICE_NAME` | `<appId>` | Only set if absent and `appId` is non-empty. |
| `OTEL_RESOURCE_ATTRIBUTES` | `wendy.app.name=<appId>` | Only set if absent and `appId` is non-empty. |

`OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` set the `service.name` and
`wendy.app.name` resource attributes that `wendy device logs --app <id>` filters
on, so telemetry an app exports directly via OTLP is attributed to its app id
without the app having to hardcode it. (Container stdout/stderr is already
stamped with these attributes by the agent's log bridge.) The identity variables
are set even when the image preconfigured a custom endpoint, so an app pointing
at its own collector still produces app-filterable logs.

The agent uses the OTLP receiver port (`4317`, gRPC) for the injected endpoint;
an HTTP/protobuf receiver is also available on `4318` for clients that prefer it.

## Processing (TODO: @martien)

TODO