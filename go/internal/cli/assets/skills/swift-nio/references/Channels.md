# Channels in SwiftNIO

> Reference: [Using SwiftNIO - Channels](https://swiftonserver.com/using-swiftnio-channels/)

## What is a Channel?

A **Channel** represents anything capable of I/O operations in SwiftNIO. While commonly used for network sockets, Channels can represent:

- TCP connections
- UDP connections
- Unix Domain Sockets
- Pipes
- Serial USB connections

A Channel is fundamentally a protocol that any connection can conform to.

## Channel Anatomy

### Key Properties

- `localAddress` - The local peer address (optional, as not all Channels are network connections)
- `remoteAddress` - The remote peer address (optional)
- `eventLoop` - The EventLoop this Channel runs on
- `pipeline` - The ChannelPipeline that processes all data

### ChannelPipeline

Every Channel has a `ChannelPipeline`. The pipeline processes all data sent and received by the Channel. Think of it as an array of `ChannelHandler`s that are called in order.

Each ChannelHandler is usually responsible for a specific task:
- `NIOSSLHandler` - Encrypts and decrypts data using TLS
- HTTP/1 parser handler - Parses HTTP/1 requests
- HTTP response serializer - Serializes HTTP responses

## Data Flow in Pipelines

### Inbound Data (Reading)

When data is received from the network:

```
Head → Handler1 → Handler2 → Handler3 → Tail
       (Inbound)   (Inbound)   (Inbound)
```

1. Data enters at the **head** of the pipeline
2. Only `ChannelInboundHandler`s are called
3. Each handler can transform or change the type of data
4. Data flows front-to-back, ending at the **tail**

### Outbound Data (Writing)

When data is sent to the network:

```
Head ← Handler1 ← Handler2 ← Handler3 ← Tail
       (Outbound)  (Outbound)  (Outbound)
```

1. Data enters at the **tail** of the pipeline
2. Only `ChannelOutboundHandler`s are called
3. Each handler can transform the data
4. Data flows back-to-front, ending at the **head**

## Channel Handlers

### InboundHandler Types

An `ChannelInboundHandler` specifies two associated types:

- **InboundIn** - The input type when reading data (e.g., `ByteBuffer`)
- **InboundOut** - The output type after processing (e.g., `HTTPServerRequestPart`)

```swift
// Example: HTTP parser handler
// InboundIn: ByteBuffer (raw bytes)
// InboundOut: HTTPServerRequestPart (parsed HTTP)
```

### OutboundHandler Types

A `ChannelOutboundHandler` also has two associated types:

- **OutboundIn** - The type of data the handler accepts
- **OutboundOut** - The type of data the handler emits

### Type Safety Warning

**Critical**: The types between handlers must match:
- InboundOut of handler N must match InboundIn of handler N+1
- OutboundOut of handler N must match OutboundIn of handler N-1

If types don't match, **SwiftNIO will crash at runtime**.

### Passing Data to Next Handler

When a handler processes data, it passes transformed data using `fireChannelRead` on the `ChannelHandlerContext`:

```swift
func channelRead(context: ChannelHandlerContext, data: NIOAny) {
    let input = unwrapInboundIn(data)
    let output = transform(input)
    context.fireChannelRead(wrapInboundOut(output))
}
```

## Creating a TCP Server with Structured Concurrency

### ServerBootstrap

Use `ServerBootstrap` to create a TCP server:

```swift
let server = try await ServerBootstrap(group: MultiThreadedEventLoopGroup.singleton)
    .bind(
        host: "0.0.0.0",  // Listen on all interfaces
        port: 2048
    ) { channel in
        // Called for every connecting client
        channel.eventLoop.makeCompletedFuture {
            // Add handlers to the pipeline here if needed
            
            return try NIOAsyncChannel(
                wrappingChannelSynchronously: channel,
                configuration: NIOAsyncChannel.Configuration(
                    inboundType: ByteBuffer.self,
                    outboundType: ByteBuffer.self
                )
            )
        }
    }
```

Key points:
- Use `MultiThreadedEventLoopGroup.singleton` as the recommended EventLoopGroup
- `0.0.0.0` listens on all network interfaces
- Each client gets assigned to a random EventLoop for load balancing
- `NIOAsyncChannel` wraps the Channel for structured concurrency support

### Accepting Clients

```swift
try await withThrowingDiscardingTaskGroup { group in
    try await server.executeThenClose { clients in
        for try await client in clients {
            group.addTask {
                do {
                    try await handleClient(client)
                } catch {
                    // Handle error gracefully
                    // Throwing here would close the entire server!
                }
            }
        }
    }
}
```

Key points:
- Use `withThrowingDiscardingTaskGroup` to manage client connections in parallel
- `executeThenClose` provides a sequence of incoming clients
- Each client is handled in its own task for concurrency
- **Never throw from the task** - it would close the server

### Handling a Client

```swift
func handleClient(_ client: NIOAsyncChannel<ByteBuffer, ByteBuffer>) async throws {
    try await client.executeThenClose { inboundMessages, outbound in
        for try await inboundMessage in inboundMessages {
            // Echo the message back
            try await outbound.write(inboundMessage)
        }
    }
}
```

Key points:
- `executeThenClose` provides inbound message stream and outbound writer
- When the client closes, the inbound sequence ends
- The connection is automatically cleaned up when the function returns

## NIOAsyncChannel

`NIOAsyncChannel` is the modern way to work with Channels using structured concurrency:

```swift
NIOAsyncChannel<Inbound, Outbound>
```

- **Inbound** - The type of messages you receive
- **Outbound** - The type of messages you send

### Configuration

```swift
NIOAsyncChannel.Configuration(
    inboundType: ByteBuffer.self,
    outboundType: ByteBuffer.self
)
```

### executeThenClose Pattern

The standard pattern for working with NIOAsyncChannel:

```swift
try await channel.executeThenClose { inbound, outbound in
    // inbound: NIOAsyncChannelInboundStream<Inbound>
    // outbound: NIOAsyncChannelOutboundWriter<Outbound>
    
    for try await message in inbound {
        // Process message
        try await outbound.write(response)
    }
}
// Channel is automatically closed when this returns
```