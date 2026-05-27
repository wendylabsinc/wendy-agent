Wendy devices produce their structured logs, metrics and tracing through [OpenTelemetry](../wendy-agent/otel.md).

These logs are received by a local on-device collector, which forwards the stream to other targets. One of those targets is the Wendy Cloud.

> **NOTE (nit)**: This **should** re-use an [existing gRPC connection](connectivity.md) between Wendy-Agent and Wendy Cloud.

The OTel spec is a standard wire protocol, also based on gRPC, that we can implement and codegen. Wendy Cloud then collects the OTel data, and "processes" it. Any queries over structured logs, visualisation and forwarding of data happens on these streams.

## Processing (TODO: @martien)

TODO