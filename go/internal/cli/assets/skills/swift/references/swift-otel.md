# Swift OTel

Swift OTel is the preferred OpenTelemetry backend for Swift server applications on Linux. It provides OTLP (OpenTelemetry Protocol) export for Swift's observability ecosystem, integrating with:

- **Swift Log** - Logging
- **Swift Metrics** - Metrics collection
- **Swift Distributed Tracing** - Distributed tracing

This package is a wire-protocol backend, not a full OTel SDK. It keeps dependencies minimal while providing seamless integration with Swift's lightweight observability APIs.

## Installation

Add to `Package.swift`:

```swift
dependencies: [
    .package(url: "https://github.com/swift-otel/swift-otel.git", from: "1.0.0")
]
```

Add the product to your target:

```swift
.target(
    name: "MyApp",
    dependencies: [
        .product(name: "OTel", package: "swift-otel")
    ]
)
```

### Package Traits

Use traits to reduce build time by only including needed exporters:

```swift
.package(
    url: "https://github.com/swift-otel/swift-otel.git",
    from: "1.0.0",
    traits: ["OTLPHTTP"]  // or "OTLPGRPC"
)
```

## Quick Start

### One-Line Bootstrap

```swift
import OTel

@main
struct MyApp {
    static func main() async throws {
        let observability = try OTel.bootstrap()

        try await withThrowingTaskGroup(of: Void.self) { group in
            group.addTask {
                try await observability.run()
            }

            // Your application logic here
            group.addTask {
                try await runServer()
            }

            try await group.next()
            group.cancelAll()
        }
    }
}
```

### With ServiceLifecycle

```swift
import OTel
import ServiceLifecycle

@main
struct MyApp {
    static func main() async throws {
        let observability = try OTel.bootstrap()

        let serviceGroup = ServiceGroup(
            services: [observability, myServer],
            logger: logger
        )

        try await serviceGroup.run()
    }
}
```

## Configuration

### Environment Variables

Swift OTel follows OpenTelemetry environment variable specifications:

```bash
# Service identification
export OTEL_SERVICE_NAME=my-service

# Exporter endpoint
export OTEL_EXPORTER_OTLP_ENDPOINT=https://otel-collector.example.com:4318

# Protocol (http/protobuf or grpc)
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf

# Individual signal endpoints
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=https://collector:4318/v1/traces
export OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=https://collector:4318/v1/metrics
export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=https://collector:4318/v1/logs

# Console exporter for development
export OTEL_LOGS_EXPORTER=console
export OTEL_TRACES_EXPORTER=console
export OTEL_METRICS_EXPORTER=console
```

### Code-Based Configuration

```swift
import OTel

var config = OTel.Configuration.default

// Service resource
config.resource.serviceName = "my-service"
config.resource.serviceVersion = "1.0.0"

// Traces configuration
config.traces.exporter = .otlp
config.traces.otlpExporter.endpoint = "https://otel-collector.example.com:4317"
config.traces.otlpExporter.protocol = .grpc
config.traces.otlpExporter.compression = .gzip
config.traces.otlpExporter.timeout = .seconds(3)

// Metrics configuration
config.metrics.exporter = .otlp
config.metrics.otlpExporter.endpoint = "https://otel-collector.example.com:4317"

// Logs configuration
config.logs.exporter = .otlp
config.logs.otlpExporter.endpoint = "https://otel-collector.example.com:4317"

let observability = try OTel.bootstrap(configuration: config)
```

### Hybrid Configuration

Combine code and environment variables. Environment variables take precedence:

```swift
// Set defaults in code
var config = OTel.Configuration.default
config.traces.otlpExporter.timeout = .seconds(5)

// Environment variables override:
// OTEL_EXPORTER_OTLP_ENDPOINT will override the endpoint
let observability = try OTel.bootstrap(configuration: config)
```

## Export Protocols

### HTTP/Protobuf (Default)

Default endpoint: `http://localhost:4318`

```swift
config.traces.otlpExporter.protocol = .httpProtobuf
config.traces.otlpExporter.endpoint = "https://collector:4318"
```

### gRPC

Default endpoint: `http://localhost:4317`

```swift
config.traces.otlpExporter.protocol = .grpc
config.traces.otlpExporter.endpoint = "https://collector:4317"
```

## Using the Observability APIs

Once bootstrapped, use Swift's standard observability APIs:

### Logging (Swift Log)

```swift
import Logging

let logger = Logger(label: "com.example.myapp")

logger.info("Request received", metadata: [
    "request.id": "\(requestId)",
    "user.id": "\(userId)"
])
```

### Metrics (Swift Metrics)

```swift
import Metrics

// Counter
let requestCounter = Counter(label: "http.requests.total")
requestCounter.increment()

// Gauge
let activeConnections = Gauge(label: "connections.active")
activeConnections.record(42)

// Histogram
let requestDuration = Timer(label: "http.request.duration")
requestDuration.recordMilliseconds(150)
```

### Tracing (Swift Distributed Tracing)

```swift
import Tracing

try await withSpan("handleRequest") { span in
    span.attributes["http.method"] = "GET"
    span.attributes["http.url"] = "/api/users"

    let users = try await withSpan("fetchUsers") { childSpan in
        try await database.query("SELECT * FROM users")
    }

    span.attributes["http.status_code"] = 200
    return users
}
```

## Framework Integration

### Hummingbird

```swift
import Hummingbird
import OTel

let observability = try OTel.bootstrap()

let router = Router()
router.middlewares.add(TracingMiddleware())
router.middlewares.add(MetricsMiddleware())
router.middlewares.add(LogRequestsMiddleware())

router.get("/health") { _, _ in
    "OK"
}

let app = Application(router: router)

let serviceGroup = ServiceGroup(
    services: [observability, app],
    logger: logger
)

try await serviceGroup.run()
```

### Vapor

```swift
import Vapor
import OTel

let observability = try OTel.bootstrap()

let app = try await Application.make()
app.middleware.use(TracingMiddleware())

// Configure routes...

let serviceGroup = ServiceGroup(
    services: [observability, app],
    logger: app.logger
)

try await serviceGroup.run()
```

## Manual Bootstrap

For advanced use cases, manually configure individual backends:

```swift
import OTel

// Create individual backends
let loggingBackend = try OTel.makeLoggingBackend(configuration: logsConfig)
let metricsBackend = try OTel.makeMetricsBackend(configuration: metricsConfig)
let tracingBackend = try OTel.makeTracingBackend(configuration: tracesConfig)

// Bootstrap Swift Log with custom handler
LoggingSystem.bootstrap { label in
    var handler = StreamLogHandler.standardOutput(label: label)
    // Add OTel as secondary handler
    return MultiplexLogHandler([handler, loggingBackend.makeLogHandler(label: label)])
}
```

## Console Exporter

For local development, export to console instead of a collector:

```swift
var config = OTel.Configuration.default
config.logs.exporter = .console
config.traces.exporter = .console
config.metrics.exporter = .console

let observability = try OTel.bootstrap(configuration: config)
```

Or via environment:

```bash
OTEL_LOGS_EXPORTER=console OTEL_TRACES_EXPORTER=console swift run
```

## Best Practices

### 1. Bootstrap Early

Initialize observability before other services:

```swift
@main
struct MyApp {
    static func main() async throws {
        // Bootstrap observability first
        let observability = try OTel.bootstrap()

        // Then initialize other services
        let database = try await Database.connect()
        let server = MyServer(database: database)

        let serviceGroup = ServiceGroup(
            services: [observability, server],
            logger: logger
        )

        try await serviceGroup.run()
    }
}
```

### 2. Use Structured Metadata

```swift
logger.info("Order processed", metadata: [
    "order.id": "\(orderId)",
    "order.total": "\(total)",
    "customer.id": "\(customerId)"
])
```

### 3. Propagate Trace Context

Trace context propagates automatically through async/await:

```swift
try await withSpan("parentOperation") { _ in
    // Child spans automatically linked
    try await withSpan("childOperation") { _ in
        // Trace context flows through
    }
}
```

### 4. Set Resource Attributes

```swift
var config = OTel.Configuration.default
config.resource.serviceName = "order-service"
config.resource.serviceVersion = "2.1.0"
config.resource.deploymentEnvironment = "production"
config.resource.attributes["host.name"] = hostname
```

## Platform Support

| Platform | Status |
|----------|--------|
| Linux | Fully supported (preferred) |
| macOS | Supported |
| iOS/tvOS/watchOS | Not targeted (use Apple's observability tools) |
