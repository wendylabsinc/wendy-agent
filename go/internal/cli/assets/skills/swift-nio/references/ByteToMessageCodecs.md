# ByteToMessage and MessageToByte Codecs

SwiftNIO provides codec abstractions for transforming raw bytes into structured messages (decoding) and vice versa (encoding). These are essential for implementing binary protocols.

## Overview

```
Network ──ByteBuffer──> [ByteToMessageDecoder] ──Messages──> Application
Network <──ByteBuffer── [MessageToByteEncoder] <──Messages── Application
```

The codec handlers sit in the ChannelPipeline and handle the translation between raw bytes and typed messages.

## ByteToMessageDecoder

Transforms incoming `ByteBuffer` data into structured messages. SwiftNIO provides `ByteToMessageHandler` that wraps your decoder implementation.

### Protocol Requirements

```swift
public protocol ByteToMessageDecoder: NIOSingleStepByteToMessageDecoder {
    associatedtype InboundOut

    /// Decode from the buffer, appending zero or more messages to `out`
    mutating func decode(context: ChannelHandlerContext, buffer: inout ByteBuffer) throws -> DecodingState

    /// Called when the channel becomes inactive - decode any remaining data
    mutating func decodeLast(context: ChannelHandlerContext, buffer: inout ByteBuffer, seenEOF: Bool) throws -> DecodingState
}

public enum DecodingState {
    case needMoreData  // Buffer doesn't have enough data yet
    case `continue`    // Successfully decoded, try to decode more
}
```

### Basic Implementation Pattern

```swift
struct LengthPrefixedDecoder: ByteToMessageDecoder {
    typealias InboundOut = ByteBuffer

    mutating func decode(context: ChannelHandlerContext, buffer: inout ByteBuffer) throws -> DecodingState {
        // Use built-in helper: reads UInt32 length, then that many bytes
        guard let messageBuffer = buffer.readLengthPrefixedSlice(as: UInt32.self) else {
            return .needMoreData
        }

        // Fire the decoded message to the next handler
        context.fireChannelRead(wrapInboundOut(messageBuffer))
        return .continue
    }

    mutating func decodeLast(context: ChannelHandlerContext, buffer: inout ByteBuffer, seenEOF: Bool) throws -> DecodingState {
        // Handle any remaining data at connection close
        try decode(context: context, buffer: &buffer)
    }
}
```

### Adding to Pipeline

```swift
try channel.pipeline.syncOperations.addHandler(
    ByteToMessageHandler(LengthPrefixedDecoder())
)
```

## MessageToByteEncoder

Transforms outbound messages into `ByteBuffer` data for transmission.

### Protocol Requirements

```swift
public protocol MessageToByteEncoder {
    associatedtype OutboundIn

    /// Encode a message into the output buffer
    func encode(data: OutboundIn, out: inout ByteBuffer) throws
}
```

### Basic Implementation Pattern

```swift
struct LengthPrefixedEncoder: MessageToByteEncoder {
    typealias OutboundIn = ByteBuffer

    func encode(data: ByteBuffer, out: inout ByteBuffer) throws {
        // Use built-in helper: writes length prefix then payload
        out.writeLengthPrefixed(as: UInt32.self) { out in
            out.writeImmutableBuffer(data)
            return out.readableBytes
        }
    }
}
```

### Adding to Pipeline

```swift
// Only use syncOperations when already on the channel's EventLoop
try channel.pipeline.syncOperations.addHandler(
    MessageToByteHandler(LengthPrefixedEncoder())
)
```

## Advanced Patterns

### Protocol with Message ID + Length

Many protocols use a message identifier followed by length-prefixed payload:

```swift
struct ProtocolMessageDecoder: ByteToMessageDecoder {
    typealias InboundOut = ProtocolMessage

    mutating func decode(context: ChannelHandlerContext, buffer: inout ByteBuffer) throws -> DecodingState {
        // Need at least: 1 byte message ID + 4 bytes length
        guard buffer.readableBytes >= 5 else {
            return .needMoreData
        }

        let savedIndex = buffer.readerIndex

        guard let messageID = buffer.readInteger(as: UInt8.self),
              let length = buffer.readInteger(as: Int32.self) else {
            return .needMoreData
        }

        // Length in many protocols includes itself (4 bytes)
        let payloadLength = Int(length) - 4

        guard payloadLength >= 0, buffer.readableBytes >= payloadLength else {
            buffer.moveReaderIndex(to: savedIndex)
            return .needMoreData
        }

        let message = try parseMessage(id: messageID, buffer: &buffer, length: payloadLength)
        context.fireChannelRead(wrapInboundOut(message))
        return .continue
    }
}
```

### Encoder with Message ID + Length

When encoding protocols with a message ID followed by a length-prefixed payload:

```swift
struct ProtocolMessageEncoder: MessageToByteEncoder {
    typealias OutboundIn = ProtocolMessage

    func encode(data: ProtocolMessage, out: inout ByteBuffer) throws {
        // Write message identifier
        out.writeInteger(data.messageID.rawValue)

        // Write length-prefixed payload (length is automatically calculated)
        out.writeLengthPrefixed(as: Int32.self) { payload in
            encodePayload(data, into: &payload)
            // Return includes the 4-byte length field itself for protocols that count it
            return payload.readableBytes + 4
        }
    }
}
```

If the protocol's length field does NOT include itself:

```swift
out.writeLengthPrefixed(as: Int32.self) { payload in
    encodePayload(data, into: &payload)
    return payload.readableBytes  // Just the payload length
}
```

### Stateful Encoder with Flush Tracking

Track buffer state to optimize when clearing is needed:

```swift
struct StatefulEncoder: MessageToByteEncoder {
    typealias OutboundIn = Message

    enum State {
        case flushed
        case writable
    }

    private var state: State = .flushed

    mutating func encode(data: Message, out: inout ByteBuffer) throws {
        // Clear buffer only after flush, not between messages
        if state == .flushed {
            out.clear()
            state = .writable
        }

        // Encode message
        encodeMessage(data, into: &out)
    }

    mutating func flush() {
        state = .flushed
    }
}
```

### Multi-Message Decoding

A single buffer may contain multiple complete messages:

```swift
mutating func decode(context: ChannelHandlerContext, buffer: inout ByteBuffer) throws -> DecodingState {
    guard buffer.readableBytes >= headerSize else {
        return .needMoreData
    }

    // Parse and validate message
    guard let message = try parseMessage(from: &buffer) else {
        return .needMoreData
    }

    context.fireChannelRead(wrapInboundOut(message))

    // Return .continue to signal "try decoding another message"
    return .continue
}
```

The handler will call `decode` repeatedly until it returns `.needMoreData`.

## Common Protocol Patterns

### Null-Terminated Strings

```swift
// Reading
guard let string = buffer.readNullTerminatedString() else {
    return .needMoreData
}

// Writing (extension method - implement yourself)
extension ByteBuffer {
    mutating func writeNullTerminatedString(_ string: String) {
        writeString(string)
        writeInteger(UInt8(0))
    }
}
```

### Fixed-Size Headers

```swift
struct FixedHeaderDecoder: ByteToMessageDecoder {
    typealias InboundOut = Message

    static let headerSize = 12  // Fixed header size

    mutating func decode(context: ChannelHandlerContext, buffer: inout ByteBuffer) throws -> DecodingState {
        guard buffer.readableBytes >= Self.headerSize else {
            return .needMoreData
        }

        let savedIndex = buffer.readerIndex

        // Parse fixed header fields
        guard let version = buffer.readInteger(as: UInt16.self),
              let flags = buffer.readInteger(as: UInt16.self),
              let payloadLength = buffer.readInteger(as: UInt64.self) else {
            return .needMoreData
        }

        guard buffer.readableBytes >= Int(payloadLength) else {
            buffer.moveReaderIndex(to: savedIndex)
            return .needMoreData
        }

        // Parse payload based on header info
        let message = try parsePayload(version: version, flags: flags, length: payloadLength, buffer: &buffer)
        context.fireChannelRead(wrapInboundOut(message))
        return .continue
    }
}
```

## Error Handling

### Validation Errors

```swift
enum ProtocolError: Error {
    case invalidMessageID(UInt8)
    case messageTooLarge(Int)
    case invalidPayload(String)
}

mutating func decode(context: ChannelHandlerContext, buffer: inout ByteBuffer) throws -> DecodingState {
    // ... parse header ...

    guard length <= maxMessageSize else {
        throw ProtocolError.messageTooLarge(Int(length))
    }

    guard let messageType = MessageType(rawValue: messageID) else {
        throw ProtocolError.invalidMessageID(messageID)
    }

    // Continue parsing...
}
```

### Preserving Buffer State on Error

When parsing fails mid-message, decide whether to:
1. Reset the reader index (retry later with more data)
2. Skip the malformed data and continue
3. Throw and close the connection

```swift
mutating func decode(context: ChannelHandlerContext, buffer: inout ByteBuffer) throws -> DecodingState {
    let savedIndex = buffer.readerIndex

    do {
        let message = try parseMessage(from: &buffer)
        context.fireChannelRead(wrapInboundOut(message))
        return .continue
    } catch let error as RecoverableError {
        // Reset and wait for more data
        buffer.moveReaderIndex(to: savedIndex)
        return .needMoreData
    } catch {
        // Unrecoverable - propagate error (closes connection)
        throw error
    }
}
```

## ByteBuffer Best Practices

### Leverage Slices

```swift
// Prefer readSlice over readBytes - avoids allocation
let slice = buffer.readSlice(length: messageLength)  // Returns ByteBuffer?

// Instead of
let bytes = buffer.readBytes(length: messageLength)  // Returns [UInt8]?
```

### Endianness

ByteBuffer uses **big-endian** (network byte order) by default for all integer operations. You can explicitly select `.little` endianness when working with protocols that require it:

```swift
// Reading integers
let bigEndian = buffer.readInteger(as: UInt32.self)  // Default: big-endian
let littleEndian = buffer.readInteger(as: UInt32.self, endianness: .little)

// Writing integers
buffer.writeInteger(UInt32(42))  // Default: big-endian
buffer.writeInteger(UInt32(42), endianness: .little)

// Get/Set at specific index (doesn't move reader/writer index)
buffer.setInteger(UInt16(1234), at: 0, endianness: .big)
let value: UInt16? = buffer.getInteger(at: 0, endianness: .big)

// Length-prefixed operations also support endianness
let slice = buffer.readLengthPrefixedSlice(as: UInt32.self, endianness: .little)

buffer.writeLengthPrefixed(as: UInt16.self, endianness: .little) { payload in
    payload.writeString("Hello")
    return payload.readableBytes
}
```

Common protocol endianness:
- **Big-endian (network order)**: TCP/IP, HTTP/2, PostgreSQL, most internet protocols
- **Little-endian**: x86/ARM native, some binary file formats, some game protocols

### Efficient Buffer Accumulation

```swift
// When accumulating multiple buffers
var accumulated = ByteBuffer()

// Use writeImmutableBuffer for zero-copy append
accumulated.writeImmutableBuffer(incomingChunk)
```

### Length-Prefixed Read/Write Helpers

SwiftNIO provides built-in methods for common length-prefixed patterns:

```swift
// Reading a length-prefixed slice
// Reads an integer of type `I`, then reads that many bytes as a slice
let slice: ByteBuffer? = buffer.readLengthPrefixedSlice(as: UInt32.self)

// With endianness (default is big-endian)
let slice = buffer.readLengthPrefixedSlice(
    as: UInt16.self,
    endianness: .little
)

// Writing with length prefix
// Writes the readable bytes of `data` prefixed by its length
buffer.writeLengthPrefixed(as: UInt32.self) { buffer in
    buffer.writeString("Hello, World!")
    return buffer.readableBytes
}

// Or write an existing buffer with length prefix
var payload = ByteBuffer(string: "Hello")
buffer.writeInteger(UInt32(payload.readableBytes))
buffer.writeBuffer(&payload)
```

The `writeLengthPrefixed` closure pattern handles length calculation automatically:

```swift
// Write a message with dynamic content
buffer.writeLengthPrefixed(as: Int32.self) { payload in
    payload.writeString(message.name)
    payload.writeInteger(UInt8(0))  // null terminator
    for param in message.parameters {
        payload.writeInteger(param.rawValue)
    }
    return payload.readableBytes
}
```

### Peek Without Consuming

```swift
// Save index before speculative read
let savedIndex = buffer.readerIndex

// Try to read
guard let value = buffer.readInteger(as: UInt32.self) else {
    return .needMoreData
}

// Check condition
guard value <= maxAllowed else {
    // Reset if we need to wait for more context
    buffer.moveReaderIndex(to: savedIndex)
    return .needMoreData
}
```

## Pipeline Setup Example

Complete pipeline with decoder and encoder:

```swift
func setupPipeline(channel: Channel) -> EventLoopFuture<Void> {
    channel.eventLoop.makeCompletedFuture {
        try channel.pipeline.syncOperations.addHandlers([
            // Inbound: bytes -> messages (called first for inbound)
            ByteToMessageHandler(MyProtocolDecoder()),

            // Outbound: messages -> bytes (called first for outbound)
            MessageToByteHandler(MyProtocolEncoder()),

            // Application handler
            MyApplicationHandler()
        ])
    }
}
```

The pipeline flow:

```
Inbound:  Network -> ByteToMessageHandler -> ApplicationHandler
Outbound: Network <- MessageToByteHandler <- ApplicationHandler
```

## Testing Codecs

Use `NIOAsyncTestingChannel` for unit testing:

```swift
@Test func testDecoder() async throws {
    let channel = NIOAsyncTestingChannel()
    try await channel.pipeline.addHandler(ByteToMessageHandler(MyDecoder()))

    // Write raw bytes as if received from network
    var buffer = ByteBuffer()
    buffer.writeInteger(UInt32(5))
    buffer.writeString("Hello")

    try await channel.writeInbound(buffer)

    // Read decoded message
    let decoded = try await channel.readInbound(as: MyMessage.self)
    #expect(decoded?.payload == "Hello")
}

@Test func testEncoder() async throws {
    let channel = NIOAsyncTestingChannel()
    try await channel.pipeline.addHandler(MessageToByteHandler(MyEncoder()))

    // Write a message as if from application
    let message = MyMessage(payload: "Hello")
    try await channel.writeOutbound(message)

    // Read encoded bytes
    let buffer = try await channel.readOutbound(as: ByteBuffer.self)
    #expect(buffer?.readableBytes == 9)  // 4 bytes length + 5 bytes payload
}
```

## Common Mistakes

### 1. Not Resetting Reader Index (When Manual Parsing)

For length-prefixed data, use `readLengthPrefixedSlice` which handles this automatically.

For custom parsing where you must read multiple fields before knowing if the message is complete:

```swift
// Wrong - loses data if not enough bytes
guard let version = buffer.readInteger(as: UInt16.self),
      let length = buffer.readInteger(as: UInt32.self) else {
    return .needMoreData
}
if buffer.readableBytes < Int(length) {
    return .needMoreData  // Reader index already advanced past version+length - data lost!
}

// Correct - save and restore reader index
let savedIndex = buffer.readerIndex
guard let version = buffer.readInteger(as: UInt16.self),
      let length = buffer.readInteger(as: UInt32.self) else {
    return .needMoreData
}
if buffer.readableBytes < Int(length) {
    buffer.moveReaderIndex(to: savedIndex)  // Reset before returning
    return .needMoreData
}
```

### 2. Returning .continue Instead of .needMoreData

```swift
// Wrong - causes infinite loop
mutating func decode(...) throws -> DecodingState {
    if buffer.readableBytes == 0 {
        return .continue  // Handler will call decode again immediately!
    }
}

// Correct
mutating func decode(...) throws -> DecodingState {
    if buffer.readableBytes == 0 {
        return .needMoreData  // Wait for more network data
    }
}
```

### 3. Type Mismatch in Pipeline

```swift
// Decoder outputs: Message
struct MyDecoder: ByteToMessageDecoder {
    typealias InboundOut = Message
}

// Handler expects: String - CRASH at runtime!
class MyHandler: ChannelInboundHandler {
    typealias InboundIn = String  // Should be Message
}
```
