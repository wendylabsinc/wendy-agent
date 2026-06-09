# Swift NIO Patterns

## NIOAsyncChannel Wrapping Pattern

Wrap NIO channels for Swift Concurrency integration:

```swift
let channel = try await bootstrap
  .connect(host: host, port: port)
  .flatMapThrowing { channel in
    try NIOAsyncChannel<
      AddressedEnvelope<ByteBuffer>,
      AddressedEnvelope<ByteBuffer>
    >(wrappingChannelSynchronously: channel)
  }
  .get()

return try await channel.executeThenClose { inbound, outbound in
  let socket = UDPSocket(inbound: inbound, outbound: outbound, channel: channel.channel)
  return try await perform(socket)
}
```

## TCP Server with Client Acceptance Loop

Use `withDiscardingTaskGroup` for concurrent client handling.
Do not use `withThrowingDiscardingTaskGroup` as it will close the server on any client connection error.

```swift
public struct TCPServer<
  InboundMessage: Sendable,
  OutboundMessage: Sendable
>: DuplexServerProtocol {
  private nonisolated let inbound:
    NIOAsyncChannelInboundStream<NIOAsyncChannel<InboundMessage, OutboundMessage>>

  public nonisolated func withEachClient(
    _ acceptClient: @Sendable @escaping (Client) async throws(CancellationError) -> Void
  ) async throws(ConnectionError) {
    try await withDiscardingTaskGroup { group in
      for try await client in inbound {
        group.addTask {
          do {
            try await client.executeThenClose { inbound, outbound in
              let socket = TCPSocket(inbound: inbound, outbound: outbound)
              return try await acceptClient(socket)
            }
          } catch {
            logger.debug("Client connection closed unexpectedly")
          }
        }
      }
    }
  }
}
```

## Bootstrap Configuration Pattern

Apply parameters to bootstraps with extension methods:

```swift
let server = try await ServerBootstrap(group: .singletonMultiThreadedEventLoopGroup)
  .serverChannelOption(.backlog, value: parameters.backlog)
  .serverChannelOption(.socketOption(.so_reuseaddr), value: 1)
  .childChannelOption(.socketOption(.so_reuseaddr), value: 1)
  .childChannelInitializer { channel in
    do {
      try channel.pipeline.syncOperations.addHandlers(protocolStack.handlers())
      return channel.eventLoop.makeSucceededVoidFuture()
    } catch {
      return channel.eventLoop.makeFailedFuture(error)
    }
  }
  .bind(host: host, port: port) { client in
    return client.eventLoop.submit {
      try NIOAsyncChannel<InboundMessage, OutboundMessage>(
        wrappingChannelSynchronously: client
      )
    }
  }
```

## Channel Duplex Handler Pattern

Create bidirectional handlers with proper type aliases:

```swift
internal final class NetworkBytesDuplexHandler: ChannelDuplexHandler {
  internal typealias InboundIn = _NetworkBytes
  internal typealias InboundOut = NetworkInputBytes
  internal typealias OutboundIn = NetworkOutputBytes
  internal typealias OutboundOut = _NetworkBytes

  internal init() {}

  internal func channelRead(context: ChannelHandlerContext, data: NIOAny) {
    let buffer = unwrapInboundIn(data)
    let networkBytes = NetworkInputBytes(buffer: buffer)
    context.fireChannelRead(wrapInboundOut(networkBytes))
  }

  internal func write(
    context: ChannelHandlerContext, data: NIOAny, promise: EventLoopPromise<Void>?
  ) {
    let networkBytes = unwrapOutboundIn(data)
    context.write(wrapOutboundOut(networkBytes.buffer), promise: nil)
  }
}
```