# Database Client Design Patterns

Patterns learned from valkey-swift - an exemplary Swift database client library implementing RESP3 protocol with cluster support, connection pooling, and full Swift Concurrency integration.

## Command Protocol Pattern

Define commands as types with associated response types for compile-time safety:

```swift
public protocol ValkeyCommand: Sendable, Hashable {
    associatedtype Response: RESPTokenDecodable = RESPToken
    associatedtype Keys: Collection<ValkeyKey>

    static var name: String { get }
    var keysAffected: Keys { get }
    var isBlocking: Bool { get }
    var isReadOnly: Bool { get }
    func encode(into encoder: inout ValkeyCommandEncoder)
}

// Default implementations
extension ValkeyCommand {
    public var keysAffected: EmptyCollection<ValkeyKey> { EmptyCollection() }
    public var isBlocking: Bool { false }
    public var isReadOnly: Bool { false }
}
```

## Struct-Based Command Implementation

Each command is a struct with typed parameters and response:

```swift
public struct GET<Key: RESPStringRenderable>: ValkeyCommand {
    public typealias Response = RESPBulkString?

    public static var name: String { "GET" }
    public let key: Key
    public var keysAffected: CollectionOfOne<ValkeyKey> {
        CollectionOfOne(.init(key))
    }
    public var isReadOnly: Bool { true }

    public init(_ key: Key) {
        self.key = key
    }

    public func encode(into encoder: inout ValkeyCommandEncoder) {
        encoder.encodeArray(Self.name, key)
    }
}

public struct SET<Key: RESPStringRenderable, Value: RESPStringRenderable>: ValkeyCommand {
    public typealias Response = RESPBulkString?

    public enum Condition: RESPRenderable {
        case nx  // Only set if not exists
        case xx  // Only set if exists
    }

    public enum Expiration: RESPRenderable {
        case seconds(Int)
        case milliseconds(Int)
        case unixTimeSeconds(Date)
        case keepttl
    }

    public static var name: String { "SET" }
    public let key: Key
    public let value: Value
    public let condition: Condition?
    public let expiration: Expiration?

    public func encode(into encoder: inout ValkeyCommandEncoder) {
        encoder.encodeArray(Self.name, key, value, condition, expiration)
    }
}
```

## RESP Protocol Token Pattern

Parse protocol responses as typed tokens with recursive structure:

```swift
public struct RESPToken: Sendable {
    public enum Value: Sendable {
        case simpleString(ByteBuffer)
        case simpleError(ByteBuffer)
        case number(Int64)
        case double(Double)
        case boolean(Bool)
        case null
        case bulkString(ByteBuffer)
        case bulkError(ByteBuffer)
        case verbatimString(ByteBuffer)
        case bigNumber(ByteBuffer)
        case array(Array)
        case map(Map)
        case set(Array)
        case attribute(Map)
        case push(Array)
    }

    public struct Array: Sequence, Sendable {
        var tokens: [RESPToken]

        public func makeIterator() -> IndexingIterator<[RESPToken]> {
            tokens.makeIterator()
        }
    }

    public struct Map: Sequence, Sendable {
        var pairs: [(key: RESPToken, value: RESPToken)]

        public func makeIterator() -> IndexingIterator<[(key: RESPToken, value: RESPToken)]> {
            pairs.makeIterator()
        }
    }
}
```

## Protocol Decodable Pattern

Define protocol for types that can decode from protocol responses:

```swift
public protocol RESPTokenDecodable: Sendable {
    init(fromRESP token: RESPToken) throws(RESPDecodeError)
}

// Built-in type conformances
extension String: RESPTokenDecodable {
    public init(fromRESP token: RESPToken) throws(RESPDecodeError) {
        switch token.value {
        case .simpleString(let buffer), .bulkString(let buffer):
            guard let string = buffer.getString(at: buffer.readerIndex, length: buffer.readableBytes) else {
                throw .typeMismatch(expected: "String", got: token)
            }
            self = string
        default:
            throw .typeMismatch(expected: "String", got: token)
        }
    }
}

extension Int: RESPTokenDecodable {
    public init(fromRESP token: RESPToken) throws(RESPDecodeError) {
        switch token.value {
        case .number(let value):
            self = Int(value)
        default:
            throw .typeMismatch(expected: "Int", got: token)
        }
    }
}
```

## Protocol Renderable Pattern

Define protocol for types that can encode to protocol format:

```swift
public protocol RESPRenderable: Sendable {
    var respEntries: Int { get }
    func encode(into encoder: inout ValkeyCommandEncoder)
}

extension String: RESPRenderable {
    public var respEntries: Int { 1 }
    public func encode(into encoder: inout ValkeyCommandEncoder) {
        encoder.encodeBulkString(self)
    }
}

extension Optional: RESPRenderable where Wrapped: RESPRenderable {
    public var respEntries: Int {
        self?.respEntries ?? 0
    }
    public func encode(into encoder: inout ValkeyCommandEncoder) {
        self?.encode(into: &encoder)
    }
}

extension Array: RESPRenderable where Element: RESPRenderable {
    public var respEntries: Int {
        reduce(0) { $0 + $1.respEntries }
    }
    public func encode(into encoder: inout ValkeyCommandEncoder) {
        for element in self {
            element.encode(into: &encoder)
        }
    }
}
```

## Actor Connection with NIO Executor Alignment

Align actor executor with NIO event loop to avoid context switches:

```swift
public final actor ValkeyConnection: ValkeyClientProtocol, Sendable {
    nonisolated public let unownedExecutor: UnownedSerialExecutor

    private let channel: any Channel
    private let channelHandler: ValkeyChannelHandler

    init(channel: any Channel, handler: ValkeyChannelHandler) {
        self.channel = channel
        self.channelHandler = handler
        // Align actor executor with channel's event loop
        self.unownedExecutor = channel.eventLoop.executor.asUnownedSerialExecutor()
    }

    public func execute<Command: ValkeyCommand>(_ command: Command) async throws -> Command.Response {
        let requestID = RequestID()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                channelHandler.write(
                    command: command,
                    continuation: continuation,
                    requestID: requestID
                )
            }
        } onCancel: {
            channelHandler.cancelRequest(requestID)
        }
    }
}
```

## Pipeline Execution Pattern

Batch multiple commands for efficient network usage:

```swift
public func execute<each Command: ValkeyCommand>(
    _ command: repeat each Command
) async throws -> (repeat (each Command).Response) {
    // Encode all commands into single buffer
    var encoder = ValkeyCommandEncoder()
    var promiseCount = 0
    for command in repeat each command {
        command.encode(into: &encoder)
        promiseCount += 1
    }

    // Create promises for each response
    let promises = (0..<promiseCount).map { _ in
        channel.eventLoop.makePromise(of: RESPToken.self)
    }

    // Write batch and await responses
    channelHandler.write(buffer: encoder.buffer, promises: promises)

    var index = 0
    return (repeat try await {
        defer { index += 1 }
        let token = try await promises[index].futureResult.get()
        return try (each Command).Response(fromRESP: token)
    }())
}
```

## Transaction Pattern with MULTI/EXEC

Wrap commands in atomic transactions:

```swift
public func transaction<each Command: ValkeyCommand>(
    _ commands: repeat each Command
) async throws -> (repeat Result<(each Command).Response, ValkeyClientError>) {
    var encoder = ValkeyCommandEncoder()

    // Encode MULTI
    MULTI().encode(into: &encoder)

    // Encode all commands
    var commandCount = 0
    for command in repeat each commands {
        command.encode(into: &encoder)
        commandCount += 1
    }

    // Encode EXEC
    EXEC().encode(into: &encoder)

    // 1 MULTI + N commands + 1 EXEC
    let promises = (0..<(commandCount + 2)).map { _ in
        channel.eventLoop.makePromise(of: RESPToken.self)
    }

    channelHandler.write(buffer: encoder.buffer, promises: promises)

    // Check MULTI response
    let multiResponse = try await promises[0].futureResult.get()
    guard case .simpleString = multiResponse.value else {
        throw ValkeyClientError.transactionFailed
    }

    // EXEC response contains array of results
    let execResponse = try await promises.last!.futureResult.get()
    guard case .array(let results) = execResponse.value else {
        throw ValkeyClientError.transactionAborted
    }

    // Decode each result
    return try results.decodeExecResults(as: (repeat (each Command).Response).self)
}
```

## Hash Slot Calculation for Cluster Routing

Calculate target shard using CRC16 hash:

```swift
public struct HashSlot: RawRepresentable, Hashable, Sendable {
    public let rawValue: UInt16

    public init?(rawValue: UInt16) {
        guard rawValue < 16384 else { return nil }
        self.rawValue = rawValue
    }

    public init(key: String) {
        let hashInput = Self.hashTag(forKey: key) ?? key
        let crc = Self.crc16(hashInput.utf8)
        self.rawValue = crc % 16384
    }

    // Extract hash tag: {tag}rest -> tag
    static func hashTag(forKey key: String) -> Substring? {
        guard let openBrace = key.firstIndex(of: "{"),
              let closeBrace = key[key.index(after: openBrace)...].firstIndex(of: "}"),
              key.index(after: openBrace) != closeBrace else {
            return nil
        }
        return key[key.index(after: openBrace)..<closeBrace]
    }

    // XMODEM CRC16 implementation
    static func crc16<S: Sequence>(_ bytes: S) -> UInt16 where S.Element == UInt8 {
        var crc: UInt16 = 0
        for byte in bytes {
            crc = ((crc << 8) & 0xFF00) ^ crcTable[Int((crc >> 8) ^ UInt16(byte))]
        }
        return crc
    }
}
```

## Cluster Redirection Handling

Handle MOVED and ASK redirections:

```swift
func executeWithRedirection<Command: ValkeyCommand>(
    _ command: Command,
    maxRetries: Int = 3
) async throws -> Command.Response {
    var lastError: Error?

    for attempt in 0..<maxRetries {
        do {
            let hashSlot = HashSlot(key: command.keysAffected.first!)
            let node = try await nodeClient(for: hashSlot)
            return try await node.execute(command)
        } catch let error as ValkeyClusterRedirectionError {
            switch error {
            case .moved(let slot, let address):
                // Permanent redirect - update topology
                await triggerTopologyDiscovery()
                lastError = error

            case .ask(let slot, let address):
                // Temporary redirect - send ASKING then retry
                let node = try await nodeClient(for: address)
                _ = try await node.execute(ASKING())
                return try await node.execute(command)

            case .tryAgain:
                // Transient failure - backoff and retry
                try await Task.sleep(for: .milliseconds(100 * (attempt + 1)))
                lastError = error
            }
        }
    }

    throw lastError ?? ValkeyClusterError.maxRetriesExceeded
}
```

## State Machine Pattern for Lifecycle

Manage complex state transitions with explicit actions:

```swift
struct ValkeyClientStateMachine<Pool, Factory> {
    enum State {
        case uninitialized
        case running(NodeMap)
    }

    enum Action {
        case runNode(Pool)
        case findReplicas(Pool)
        case shutdownNodes([Pool])
        case none
    }

    private var state: State = .uninitialized

    mutating func setPrimary(_ pool: Pool) -> Action {
        switch state {
        case .uninitialized:
            state = .running(NodeMap(primary: pool))
            return .runNode(pool)
        case .running(var nodes):
            if nodes.primary === pool {
                return .none
            }
            let oldPrimary = nodes.primary
            nodes.primary = pool
            state = .running(nodes)
            return .shutdownNodes([oldPrimary])
        }
    }

    mutating func addReplicas(_ replicas: [Pool]) -> Action {
        guard case .running(var nodes) = state else {
            preconditionFailure("Cannot add replicas before primary")
        }

        let toShutdown = nodes.replicas.filter { !replicas.contains($0) }
        nodes.replicas = replicas
        state = .running(nodes)

        return toShutdown.isEmpty ? .none : .shutdownNodes(toShutdown)
    }
}
```

## Subscription Management Pattern

Track pub/sub subscriptions with reference counting:

```swift
struct ValkeySubscriptions {
    private var subscriptions: [Int: SubscriptionRef] = [:]
    private var filterMap: [ValkeySubscriptionFilter: ChannelState] = [:]
    private var nextID = 0

    enum SubscribeAction {
        case subscribe([String])
        case psubscribe([String])
        case none
    }

    mutating func add(
        filters: [ValkeySubscriptionFilter],
        continuation: AsyncStream<PushToken>.Continuation
    ) -> (id: Int, action: SubscribeAction) {
        let id = nextID
        nextID += 1

        var newChannels: [String] = []

        for filter in filters {
            if filterMap[filter] == nil {
                filterMap[filter] = ChannelState()
                newChannels.append(filter.pattern)
            }
            filterMap[filter]!.subscribers.insert(id)
        }

        subscriptions[id] = SubscriptionRef(filters: filters, continuation: continuation)

        let action: SubscribeAction
        if newChannels.isEmpty {
            action = .none
        } else if filters.first?.isPattern == true {
            action = .psubscribe(newChannels)
        } else {
            action = .subscribe(newChannels)
        }

        return (id, action)
    }

    mutating func remove(id: Int) -> UnsubscribeAction {
        guard let ref = subscriptions.removeValue(forKey: id) else {
            return .none
        }

        var channelsToUnsubscribe: [String] = []

        for filter in ref.filters {
            filterMap[filter]?.subscribers.remove(id)
            if filterMap[filter]?.subscribers.isEmpty == true {
                filterMap.removeValue(forKey: filter)
                channelsToUnsubscribe.append(filter.pattern)
            }
        }

        ref.continuation.finish()
        return channelsToUnsubscribe.isEmpty ? .none : .unsubscribe(channelsToUnsubscribe)
    }
}
```

## Connection Pool Integration

Conform connections to pool protocols:

```swift
extension ValkeyConnection: PooledConnection {
    public typealias ConnectionID = Int
}

struct ValkeyKeepAliveBehavior: ConnectionKeepAliveBehavior {
    typealias Connection = ValkeyConnection

    let frequency: Duration

    func runKeepAlive(for connection: ValkeyConnection) async throws {
        _ = try await connection.execute(PING())
    }
}

final class ValkeyClientMetrics: ConnectionPoolObservabilityDelegate {
    typealias ConnectionID = Int

    private let logger: Logger

    func connectionCreated(id: ConnectionID) {
        logger.debug("Connection created", metadata: ["id": "\(id)"])
    }

    func connectionLeased(id: ConnectionID) {
        logger.trace("Connection leased", metadata: ["id": "\(id)"])
    }

    func connectionReleased(id: ConnectionID) {
        logger.trace("Connection released", metadata: ["id": "\(id)"])
    }

    func connectionClosed(id: ConnectionID) {
        logger.debug("Connection closed", metadata: ["id": "\(id)"])
    }
}
```

## Testing with NIOAsyncTestingChannel

Create testable channel handlers without network:

```swift
@Test func testCommandExecution() async throws {
    let channel = NIOAsyncTestingChannel()
    let handler = ValkeyChannelHandler(configuration: .init())
    try await channel.pipeline.addHandler(handler)

    // Simulate connection
    handler.setConnected()

    // Execute command
    async let response = handler.execute(GET("mykey"))

    // Wait for outbound write
    let outbound = try await channel.waitForOutboundWrite(as: ByteBuffer.self)
    #expect(outbound.getString(at: 0, length: outbound.readableBytes) == "*2\r\n$3\r\nGET\r\n$5\r\nmykey\r\n")

    // Simulate server response
    var responseBuffer = ByteBuffer(string: "$5\r\nhello\r\n")
    try await channel.writeInbound(responseBuffer)

    // Verify response
    let result = try await response
    #expect(result.string == "hello")
}

@Test func testPipelineExecution() async throws {
    let channel = NIOAsyncTestingChannel()
    let handler = ValkeyChannelHandler(configuration: .init())
    try await channel.pipeline.addHandler(handler)

    // Execute pipeline
    async let responses = handler.execute(
        SET("key1", "value1"),
        GET("key1"),
        DEL("key1")
    )

    // Write mock responses
    try await channel.writeInbound(ByteBuffer(string: "+OK\r\n$6\r\nvalue1\r\n:1\r\n"))

    let (setResult, getResult, delResult) = try await responses
    #expect(setResult == "OK")
    #expect(getResult?.string == "value1")
    #expect(delResult == 1)
}
```

## Module Extension Pattern

Extend client protocol for module-specific commands:

```swift
// Define module commands in separate namespace
public enum JSON {
    public struct SET<Path: RESPStringRenderable, Value: RESPStringRenderable>: ValkeyCommand {
        public typealias Response = String

        public static var name: String { "JSON.SET" }
        public let key: String
        public let path: Path
        public let value: Value

        public func encode(into encoder: inout ValkeyCommandEncoder) {
            encoder.encodeArray(Self.name, key, path, value)
        }
    }

    public struct GET<Path: RESPStringRenderable>: ValkeyCommand {
        public typealias Response = RESPBulkString?

        public static var name: String { "JSON.GET" }
        public let key: String
        public let paths: [Path]

        public func encode(into encoder: inout ValkeyCommandEncoder) {
            encoder.encodeArray(Self.name, key, paths)
        }
    }
}

// Extend client protocol with convenience methods
extension ValkeyClientProtocol {
    @available(valkeySwift 1.0, *)
    public func jsonSet<Path: RESPStringRenderable, Value: RESPStringRenderable>(
        _ key: String,
        path: Path,
        value: Value
    ) async throws -> String {
        try await execute(JSON.SET(key: key, path: path, value: value))
    }

    @available(valkeySwift 1.0, *)
    public func jsonGet<Path: RESPStringRenderable>(
        _ key: String,
        paths: Path...
    ) async throws -> RESPBulkString? {
        try await execute(JSON.GET(key: key, paths: paths))
    }
}
```

## Error Handling Pattern

Define structured errors with typed throws:

```swift
public enum RESPDecodeError: Error, Sendable {
    case typeMismatch(expected: String, got: RESPToken)
    case invalidFormat(String)
    case outOfBounds(index: Int, count: Int)
}

public enum ValkeyClientError: Error, Sendable {
    case connectionClosed
    case connectionClosedDueToCancellation
    case timeout
    case serverError(String)
    case transactionAborted
    case clusterKeyMismatch
}

public enum ValkeyClusterRedirectionError: Error, Sendable {
    case moved(slot: HashSlot, to: ValkeyServerAddress)
    case ask(slot: HashSlot, to: ValkeyServerAddress)
    case tryAgain
    case clusterDown
}

// Use typed throws in hot paths
extension RESPToken {
    public func decode<T: RESPTokenDecodable>(as type: T.Type) throws(RESPDecodeError) -> T {
        try T(fromRESP: self)
    }
}
```

## Depth-Limited Parsing

Prevent stack overflow with nested structures:

```swift
extension ByteBuffer {
    mutating func parseRESPToken(maxDepth: Int = 100) throws -> RESPToken {
        guard maxDepth > 0 else {
            throw RESPParsingError.tooDeeplyNestedAggregatedTypes
        }

        guard let typeIdentifier = readInteger(as: UInt8.self) else {
            throw RESPParsingError.insufficientData
        }

        switch RESPTypeIdentifier(rawValue: typeIdentifier) {
        case .array:
            guard let count = readRESPInteger() else {
                throw RESPParsingError.canNotParseInteger
            }
            var elements: [RESPToken] = []
            elements.reserveCapacity(count)
            for _ in 0..<count {
                elements.append(try parseRESPToken(maxDepth: maxDepth - 1))
            }
            return RESPToken(value: .array(.init(tokens: elements)))
        // ... other cases
        }
    }
}
```
