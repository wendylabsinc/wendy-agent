# Swift Log

Swift Log is the official logging API for Swift. It provides a common logging interface that libraries and applications can use, allowing log backends to be configured at the application level.

## Log Levels

SwiftLog defines 7 levels from least to most severe:

| Level | Use Case |
|-------|----------|
| `trace` | Finest-grained debugging for hard-to-reproduce issues. Assume trace logging will not be used in production unless specifically enabled. |
| `debug` | High-value operational insights without overwhelming systems. Should not undermine production performance. |
| `info` | General informational messages about application flow. |
| `notice` | Notable events worth attention but not problematic. |
| `warning` | Actionable alerts that may require attention. |
| `error` | Error conditions that affected an operation. |
| `critical` | System-critical failures requiring immediate attention. |

## Basic Usage

```swift
import Logging

// Create a logger
let logger = Logger(label: "com.example.myapp")

// Log at different levels
logger.trace("Detailed trace information")
logger.debug("Debug information")
logger.info("Application started")
logger.notice("Notable event occurred")
logger.warning("Resource usage is high")
logger.error("Failed to connect to database")
logger.critical("System is shutting down")
```

## Structured Logging

Prefer metadata dictionaries over string interpolation for machine-parseable logs:

```swift
// Preferred: Structured metadata
logger.info("Accepted connection", metadata: [
    "connection.id": "\(connection.id)",
    "connection.peer": "\(connection.peer)",
    "connections.total": "\(connections.count)"
])

// Avoid: String interpolation
logger.info("Accepted connection \(connection.id) from \(connection.peer)")
```

Structured logging enables:
- Automated log processing and aggregation
- Correlation tracking via TraceIDs across distributed systems
- Better filtering and searching in log management systems

## Best Practices for Libraries

### Primary Levels

Libraries should primarily use `trace` and `debug` logging:

- **Trace**: Exhaustive diagnostic data for debugging hard-to-reproduce issues
- **Debug**: Valuable operational insights that won't overwhelm production systems

### When NOT to Log Errors/Warnings

Avoid logging errors or warnings when:

1. **End-users can handle the issue** - Use `throw` or return an `Error` instead
   ```swift
   // Bad: Logging an error the caller should handle
   logger.error("Invalid input provided")
   throw ValidationError.invalidInput

   // Good: Just throw, let the caller decide
   throw ValidationError.invalidInput
   ```

2. **Reporting normal operations** - Don't log every successful request
   ```swift
   // Bad: Logging expected behavior
   logger.info("Request completed successfully")

   // Good: Only log if it provides operational value
   logger.debug("Processed batch of \(count) items")
   ```

3. **The information isn't actionable** - If a library user can't do anything about it, don't log it as a warning/error

### Configuration Responsibility

Log configuration belongs to the **executable target**, not libraries:

```swift
// In your application (main.swift or App entry point):
LoggingSystem.bootstrap { label in
    StreamLogHandler.standardOutput(label: label)
}

// Libraries should NEVER do this:
// LoggingSystem.bootstrap { ... }  // Don't configure logging in a library
```

## Creating Logger Instances

### In Applications

```swift
import Logging

@main
struct MyApp {
    static func main() {
        // Bootstrap once at startup
        LoggingSystem.bootstrap { label in
            StreamLogHandler.standardOutput(label: label)
        }

        let logger = Logger(label: "com.example.myapp")
        logger.info("Application starting")
    }
}
```

### In Libraries

```swift
import Logging

public struct MyLibrary {
    private let logger: Logger

    public init(logger: Logger = Logger(label: "com.example.mylibrary")) {
        self.logger = logger
    }

    public func doWork() {
        logger.trace("Starting work")
        // ... implementation
        logger.debug("Work completed")
    }
}
```

## Log Metadata

### Adding Context

```swift
var logger = Logger(label: "com.example.myapp")

// Set metadata that persists across log calls
logger[metadataKey: "request-id"] = "\(requestId)"
logger[metadataKey: "user-id"] = "\(userId)"

// All subsequent logs include this metadata
logger.info("Processing request")  // Includes request-id and user-id
```

### One-time Metadata

```swift
// Add metadata for a single log call
logger.info("Database query executed", metadata: [
    "query": "\(sanitizedQuery)",
    "duration_ms": "\(duration)"
])
```

## Custom Log Handlers

Implement `LogHandler` protocol to create custom backends:

```swift
import Logging

struct MyCustomLogHandler: LogHandler {
    var metadata: Logger.Metadata = [:]
    var logLevel: Logger.Level = .info

    subscript(metadataKey key: String) -> Logger.Metadata.Value? {
        get { metadata[key] }
        set { metadata[key] = newValue }
    }

    func log(
        level: Logger.Level,
        message: Logger.Message,
        metadata: Logger.Metadata?,
        source: String,
        file: String,
        function: String,
        line: UInt
    ) {
        // Custom logging implementation
        print("[\(level)] \(message)")
    }
}
```

## Integration with Distributed Tracing

For distributed systems, include correlation IDs in metadata:

```swift
func handleRequest(_ request: Request) async {
    var logger = Logger(label: "com.example.api")
    logger[metadataKey: "trace-id"] = request.traceId
    logger[metadataKey: "span-id"] = UUID().uuidString

    logger.info("Handling request", metadata: [
        "path": "\(request.path)",
        "method": "\(request.method)"
    ])

    // Pass logger to downstream services
    await processRequest(request, logger: logger)
}
```
