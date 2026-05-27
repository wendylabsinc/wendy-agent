# SwiftNIO Integration Patterns

Advanced patterns for integrating SwiftNIO with modern Swift code, extracted from production frameworks.

## Channel Handler Abstraction

### ServerChildChannel Protocol

Abstract NIO channels with a protocol for different implementations:

```swift
public protocol ServerChildChannel: Sendable {
    associatedtype Value: ServerChildChannelValue

    // Setup phase: configure the channel and return wrapped value
    func setup(channel: any Channel, logger: Logger) -> EventLoopFuture<Value>

    // Handle phase: process the channel using async/await
    func handle(value: Value, logger: Logger) async
}

public protocol ServerChildChannelValue: Sendable {
    var channel: any Channel { get }
}

// HTTP1 implementation
public struct HTTP1Channel: ServerChildChannel {
    public typealias Value = NIOAsyncChannel<HTTPRequestPart, HTTPResponsePart>

    public struct Configuration: Sendable {
        public var additionalChannelHandlers: @Sendable () -> [any RemovableChannelHandler]
        public var idleTimeout: TimeAmount?

        public init(
            additionalChannelHandlers: @autoclosure @escaping @Sendable () -> [any RemovableChannelHandler] = [],
            idleTimeout: TimeAmount? = nil
        ) {
            self.additionalChannelHandlers = additionalChannelHandlers
            self.idleTimeout = idleTimeout
        }
    }

    public func setup(channel: any Channel, logger: Logger) -> EventLoopFuture<Value> {
        channel.eventLoop.makeCompletedFuture {
            try channel.pipeline.syncOperations.addHandlers(/* configure pipeline */)
            return try NIOAsyncChannel(
                wrappingChannelSynchronously: channel,
                configuration: .init(
                    inboundType: HTTPRequestPart.self,
                    outboundType: HTTPResponsePart.self
                )
            )
        }
    }

    public func handle(value: Value, logger: Logger) async {
        // Process using async/await
    }
}
```

**Benefits:**
- Encapsulates complex NIO operations
- Type-safe channel handling
- Composable configurations
- Hides implementation details

## NIOAsyncChannel Integration

### Structured Concurrency with Channels

```swift
public func handle(value: Value, logger: Logger) async {
    do {
        try await value.executeThenClose { inbound, outbound in
            // executeThenClose guarantees cleanup
            await withDiscardingTaskGroup { group in
                let responseWriter = ResponseWriter(outbound: outbound)

                for try await part in inbound {
                    switch part {
                    case .head(let head):
                        // Accumulate request metadata
                    case .body(let buffer):
                        // Accumulate body
                    case .end:
                        // Spawn handler task
                        group.addTask {
                            await self.handleRequest(responseWriter)
                        }
                    }
                }
            }
        }
    } catch {
        logger.trace("Connection closed: \(error)")
    }
}
```

**Key points:**
- `executeThenClose` ensures channel cleanup
- `withDiscardingTaskGroup` for fire-and-forget request handling
- No task leaks due to structured concurrency

## ByteBuffer Patterns

### Efficient Request Body Accumulation

```swift
struct RequestBodyAccumulator {
    private var buffer = ByteBuffer()
    private let maxSize: Int

    init(maxSize: Int = 1_000_000) {
        self.maxSize = maxSize
    }

    mutating func append(_ chunk: ByteBuffer) throws {
        guard buffer.readableBytes + chunk.readableBytes <= maxSize else {
            throw HTTPError(.payloadTooLarge)
        }

        if buffer.readableBytes == 0 {
            // Empty buffer - just replace it (no copy needed)
            buffer = chunk
        } else {
            buffer.writeImmutableBuffer(chunk)
        }
    }

    consuming func finish() -> ByteBuffer {
        buffer
    }
}
```

## Graceful Shutdown Integration

Graceful shutdown is crucial in server applications to ensure that in-progress requests are handled correctly and no data is lost or corrupted during shutdown. Without graceful shutdown, a server may abruptly close connections, interrupting active client communications and potentially leaving partially-processed work in an inconsistent state. 

Graceful shutdown procedures allow the server to:
- Stop accepting new connections
- Wait for all ongoing requests to complete
- Release all resources cleanly (such as sockets, file descriptors, or database connections)
- Signal other coordinated services properly before exiting

In Swift apps, properly implementing graceful shutdown helps maintain reliability and integrity of services, avoids confusing errors for clients, and makes updates or deployments safer with zero-downtime handovers.


### Service-Based Server Shutdown

```swift
import ServiceLifecycle

extension Server {
    public func shutdownGracefully() async throws {
        guard case .running(let asyncChannel, let quiescingHelper) = self.state else {
            return
        }

        self.state = .shuttingDown(shutdownPromise: promise)

        // Initiate graceful shutdown
        try await quiescingHelper.initiateShutdown()

        self.state = .shutdown
    }
}

// Integration with ServiceLifecycle
await withGracefulShutdownHandler {
    try await server.run()
} onGracefulShutdown: {
    Task {
        try await server.shutdownGracefully()
    }
}
```

## EventLoop Best Practices

### Non-Blocking Operations

```swift
// NEVER do this - blocks the EventLoop
func bad(channel: Channel) {
    let result = someBlockingOperation()  // Blocks EventLoop thread
    channel.write(result)
}

// DO this - offload to thread pool
func good(channel: Channel, threadPool: NIOThreadPool) async throws {
    let result = try await threadPool.runIfActive(eventLoop: channel.eventLoop) {
        someBlockingOperation()
    }
    try await channel.writeAndFlush(result)
}
```