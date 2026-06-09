# PostgreSQL Client Design Patterns

Patterns learned from postgres-nio - an exemplary Swift PostgreSQL client library implementing the PostgreSQL wire protocol with full async/await support, connection pooling, and type-safe query building.

## String Interpolation for SQL Injection Prevention

Use Swift's `ExpressibleByStringInterpolation` to convert interpolated values to parameter bindings:

```swift
public struct PostgresQuery: Sendable, Hashable, ExpressibleByStringInterpolation {
    public var sql: String
    public var binds: PostgresBindings

    // Explicit unsafe SQL for dynamic queries
    public init(unsafeSQL sql: String, binds: PostgresBindings = PostgresBindings()) {
        self.sql = sql
        self.binds = binds
    }

    public struct StringInterpolation: StringInterpolationProtocol, Sendable {
        var sql: String
        var binds: PostgresBindings

        public init(literalCapacity: Int, interpolationCount: Int) {
            self.sql = ""
            self.sql.reserveCapacity(literalCapacity)
            self.binds = PostgresBindings(capacity: interpolationCount)
        }

        // Literal SQL text passes through unchanged
        public mutating func appendLiteral(_ literal: String) {
            self.sql.append(contentsOf: literal)
        }

        // Interpolated values become parameter bindings
        public mutating func appendInterpolation<Value: PostgresThrowingDynamicTypeEncodable>(
            _ value: Value
        ) throws {
            try self.binds.append(value, context: .default)
            self.sql.append(contentsOf: "$\(self.binds.count)")
        }

        // Explicit unescaped interpolation for identifiers
        public mutating func appendInterpolation(unescaped value: String) {
            self.sql.append(contentsOf: value)
        }
    }
}

// Usage - SQL injection impossible
let userId = 123
let query: PostgresQuery = "SELECT * FROM users WHERE id = \(userId)"
// Generates: sql = "SELECT * FROM users WHERE id = $1", binds = [123]
```

## Protocol Hierarchy for Encoding/Decoding

Design tiered protocols for different encoding guarantees:

```swift
// Base: runtime-determined type, may throw
public protocol PostgresThrowingDynamicTypeEncodable: Sendable {
    func encode<JSONEncoder: PostgresJSONEncoder>(
        into byteBuffer: inout ByteBuffer,
        context: PostgresEncodingContext<JSONEncoder>
    ) throws

    var psqlType: PostgresDataType { get }
    var psqlFormat: PostgresFormat { get }
}

// Non-throwing variant for string interpolation
public protocol PostgresDynamicTypeEncodable: PostgresThrowingDynamicTypeEncodable {
    func encode<JSONEncoder: PostgresJSONEncoder>(
        into byteBuffer: inout ByteBuffer,
        context: PostgresEncodingContext<JSONEncoder>
    )
}

// Static type info known at compile time
public protocol PostgresEncodable: PostgresThrowingDynamicTypeEncodable {
    static var psqlType: PostgresDataType { get }
    static var psqlFormat: PostgresFormat { get }
}

// Non-throwing + static type info
public protocol PostgresNonThrowingEncodable: PostgresEncodable, PostgresDynamicTypeEncodable {}

// Decoding protocol
public protocol PostgresDecodable: Sendable {
    init(
        from buffer: inout ByteBuffer,
        type: PostgresDataType,
        format: PostgresFormat,
        context: PostgresDecodingContext<some PostgresJSONDecoder>
    ) throws
}

// Convenience alias
public typealias PostgresCodable = PostgresEncodable & PostgresDecodable
```

## Length-Prefixed Binary Encoding

Write length-prefixed data with placeholder and backfill:

```swift
extension PostgresEncodable {
    @inlinable
    public func encodeRaw<JSONEncoder: PostgresJSONEncoder>(
        into buffer: inout ByteBuffer,
        context: PostgresEncodingContext<JSONEncoder>
    ) throws {
        // Write placeholder for length (4 bytes for Int32)
        let lengthIndex = buffer.writerIndex
        buffer.writeInteger(Int32(0))

        // Record position before encoding
        let startIndex = buffer.writerIndex

        // Encode the actual value
        try self.encode(into: &buffer, context: context)

        // Calculate and write actual length
        let length = buffer.writerIndex - startIndex
        buffer.setInteger(Int32(length), at: lengthIndex)
    }
}

// Integer encoding example
extension Int32: PostgresNonThrowingEncodable {
    public static var psqlType: PostgresDataType { .int4 }
    public static var psqlFormat: PostgresFormat { .binary }

    public func encode<JSONEncoder: PostgresJSONEncoder>(
        into byteBuffer: inout ByteBuffer,
        context: PostgresEncodingContext<JSONEncoder>
    ) {
        byteBuffer.writeInteger(self)
    }
}

// Integer decoding with format handling
extension Int32: PostgresDecodable {
    public init(
        from buffer: inout ByteBuffer,
        type: PostgresDataType,
        format: PostgresFormat,
        context: PostgresDecodingContext<some PostgresJSONDecoder>
    ) throws {
        switch format {
        case .binary:
            guard buffer.readableBytes == 4,
                  let value = buffer.readInteger(as: Int32.self) else {
                throw PostgresDecodingError.failure
            }
            self = value

        case .text:
            guard let string = buffer.readString(length: buffer.readableBytes),
                  let value = Int32(string) else {
                throw PostgresDecodingError.failure
            }
            self = value
        }
    }
}
```

## Variadic Generics for Row Decoding

Decode multiple columns type-safely using parameter packs:

```swift
extension PostgresRow {
    public func decode<each Column: PostgresDecodable>(
        _ columnType: (repeat each Column).Type,
        context: PostgresDecodingContext<some PostgresJSONDecoder> = .default,
        file: String = #fileID,
        line: Int = #line
    ) throws -> (repeat each Column) {
        var cellIterator = self.data.makeIterator()
        var columnIterator = self.columns.makeIterator()
        var columnIndex = 0

        return (repeat try Self.decodeNextColumn(
            (each Column).self,
            cellIterator: &cellIterator,
            columnIterator: &columnIterator,
            columnIndex: &columnIndex,
            context: context,
            file: file,
            line: line
        ))
    }

    private static func decodeNextColumn<Column: PostgresDecodable>(
        _ columnType: Column.Type,
        cellIterator: inout IndexingIterator<DataRow>,
        columnIterator: inout IndexingIterator<[RowDescription.Column]>,
        columnIndex: inout Int,
        context: PostgresDecodingContext<some PostgresJSONDecoder>,
        file: String,
        line: Int
    ) throws -> Column {
        defer { columnIndex += 1 }

        guard let cell = cellIterator.next(),
              let column = columnIterator.next() else {
            throw PostgresDecodingError.columnOutOfBounds(
                index: columnIndex,
                file: file,
                line: line
            )
        }

        return try Column._decodeRaw(
            from: cell,
            type: column.dataType,
            format: column.format,
            context: context
        )
    }
}

// Parameter pack length computation
enum _PostgresDecodablePack<each T: PostgresDecodable> {
    @inlinable
    static var count: Int {
        MemoryLayout<(repeat (each T).Type)>.size / MemoryLayout<Any.Type>.stride
    }
}

// Usage
let row: PostgresRow = ...
let (id, name, email) = try row.decode((Int.self, String.self, String.self))
```

## Prepared Statement Protocol Pattern

Define type-safe prepared statements as conforming types:

```swift
public protocol PostgresPreparedStatement: Sendable {
    associatedtype Row

    /// The prepared statement's name (defaults to type name)
    static var name: String { get }

    /// The SQL to prepare
    static var sql: String { get }

    /// Optional explicit parameter types (inferred if empty)
    static var bindingDataTypes: [PostgresDataType] { get }

    /// Create parameter bindings from instance properties
    func makeBindings() -> PostgresBindings

    /// Decode a row into the Row type
    func decodeRow(_ row: PostgresRow) throws -> Row
}

// Default implementations
extension PostgresPreparedStatement {
    public static var name: String { String(reflecting: Self.self) }
    public static var bindingDataTypes: [PostgresDataType] { [] }
}

// Example implementation
struct GetUserByEmail: PostgresPreparedStatement {
    typealias Row = (Int, String, Date)

    static let sql = "SELECT id, name, created_at FROM users WHERE email = $1"

    var email: String

    func makeBindings() -> PostgresBindings {
        var bindings = PostgresBindings()
        bindings.append(self.email)
        return bindings
    }

    func decodeRow(_ row: PostgresRow) throws -> Row {
        try row.decode(Row.self)
    }
}

// Execution on connection
extension PostgresConnection {
    public func execute<Statement: PostgresPreparedStatement>(
        _ preparedStatement: Statement,
        logger: Logger
    ) async throws -> AsyncThrowingMapSequence<PostgresRowSequence, Statement.Row> {
        // Prepare once, execute with bindings
        let prepared = try await self.prepare(
            Statement.sql,
            name: Statement.name,
            bindingDataTypes: Statement.bindingDataTypes
        )
        return try await self.execute(
            prepared,
            bindings: preparedStatement.makeBindings(),
            logger: logger
        ).map { try preparedStatement.decodeRow($0) }
    }
}
```

## Hierarchical State Machine Pattern

Compose state machines for complex protocol flows:

```swift
struct ConnectionStateMachine {
    enum State {
        case initialized
        case sslRequestSent(SSlRequestContext)
        case sslNegotiated
        case sslHandlerAdded
        case waitingToStartAuthentication
        case authenticating(AuthenticationStateMachine)
        case authenticated(BackendKeyData?, TLSConfiguration?)
        case readyForQuery(ConnectionContext)
        case extendedQuery(ExtendedQueryStateMachine, ConnectionContext)
        case closeCommand(CloseStateMachine, ConnectionContext)
        case closing(CleanUpContext)
        case closed

        // Sentinel to prevent COW during mutations
        case modifying
    }

    enum ConnectionAction {
        case sendStartupMessage(AuthContext)
        case sendSSLRequest
        case establishSSLConnection
        case sendPasswordMessage(PasswordAuthContext)
        case sendSASLInitialResponse(SASLAuthContext)
        case provideAuthenticationContext
        case forwardRows([DataRow])
        case forwardStreamComplete(Result<StatementSummary, Error>)
        case closeConnectionAndCleanup(CleanUpContext)
        case read
        case wait
    }

    private var state: State = .initialized
    private var taskQueue: CircularBuffer<PSQLTask> = .init(initialCapacity: 4)

    // Each message handler validates state and returns action
    mutating func authenticationMessageReceived(
        _ message: PostgresBackendMessage.Authentication
    ) -> ConnectionAction {
        guard case .authenticating(var authState) = self.state else {
            return self.closeConnectionAndCleanup(
                .unexpectedBackendMessage(.authentication(message))
            )
        }

        self.state = .modifying
        let action = authState.authenticationMessageReceived(message)
        return self.modify(with: action)
    }

    // Translate child state machine actions
    private mutating func modify(
        with action: AuthenticationStateMachine.Action
    ) -> ConnectionAction {
        switch action {
        case .sendStartupMessage(let context):
            self.state = .waitingToStartAuthentication
            return .sendStartupMessage(context)

        case .sendPasswordMessage(let context):
            // Keep in authenticating state
            return .sendPasswordMessage(context)

        case .authenticated(let keyData, let params):
            self.state = .authenticated(keyData, params)
            return .wait

        case .reportError(let error):
            return self.setErrorAndCreateCleanupAction(error)
        }
    }
}

// Child state machine with focused responsibility
struct AuthenticationStateMachine {
    enum Action {
        case sendStartupMessage(AuthContext)
        case sendPasswordMessage(PasswordAuthContext)
        case sendSASLInitialResponse(SASLAuthContext)
        case sendSASLResponse(Data)
        case authenticated(BackendKeyData?, [String: String])
        case reportError(PSQLError)
    }

    enum State {
        case initialized
        case startupMessageSent
        case passwordAuthenticationSent
        case saslInitialResponseSent(SASLAuthContext)
        case saslChallengeResponseSent(SASLAuthContext)
        case authenticated
    }

    private var state: State = .initialized

    mutating func authenticationMessageReceived(
        _ message: PostgresBackendMessage.Authentication
    ) -> Action {
        switch (self.state, message) {
        case (.startupMessageSent, .ok):
            self.state = .authenticated
            return .authenticated(nil, [:])

        case (.startupMessageSent, .md5Password(let salt)):
            self.state = .passwordAuthenticationSent
            return .sendPasswordMessage(.md5(salt: salt))

        case (.saslChallengeResponseSent(let context), .saslFinal(let data)):
            // Verify server signature
            guard context.verifySASLFinal(data) else {
                return .reportError(.saslError(.serverSignatureMismatch))
            }
            self.state = .authenticated
            return .authenticated(nil, [:])

        default:
            return .reportError(.unexpectedBackendMessage(.authentication(message)))
        }
    }
}
```

## Backpressure-Aware Row Streaming

Stream rows with adaptive buffering and demand signaling:

```swift
final class PSQLRowStream: @unchecked Sendable {
    enum DownstreamState {
        case waitingForConsumer(BufferState)
        case iteratingRows(
            onRow: (PostgresRow) throws -> (),
            EventLoopPromise<Void>,
            PSQLRowsDataSource
        )
        case asyncSequence(
            NIOThrowingAsyncSequenceProducer<DataRow, Error, AdaptiveRowBuffer, PSQLRowStream>.Source,
            PSQLRowsDataSource,
            onFinish: @Sendable () -> ()
        )
        case consumed(Result<StatementSummary, Error>)
    }

    private var downstreamState: DownstreamState
    private let eventLoop: any EventLoop

    // Called when rows arrive from backend
    func receive(_ rows: [DataRow]) {
        self.eventLoop.assertInEventLoop()

        switch self.downstreamState {
        case .asyncSequence(let source, let dataSource, _):
            for row in rows {
                let result = source.yield(row)
                self.executeActionBasedOnYieldResult(result, source: dataSource)
            }

        case .iteratingRows(let onRow, let promise, _):
            do {
                for row in rows {
                    try onRow(PostgresRow(data: row, lookupTable: self.lookupTable, columns: self.columns))
                }
            } catch {
                promise.fail(error)
            }

        case .waitingForConsumer(var buffer):
            buffer.append(rows)
            self.downstreamState = .waitingForConsumer(buffer)

        case .consumed:
            break // Already finished
        }
    }

    private func executeActionBasedOnYieldResult(
        _ result: NIOThrowingAsyncSequenceProducer<DataRow, Error, AdaptiveRowBuffer, PSQLRowStream>.Source.YieldResult,
        source: PSQLRowsDataSource
    ) {
        switch result {
        case .produceMore:
            source.requestMore()
        case .stopProducing:
            // Consumer buffer full - wait for demand
            break
        case .dropped:
            // Consumer cancelled
            source.cancel()
        }
    }
}

// Adaptive buffer strategy
struct AdaptiveRowBuffer: NIOAsyncSequenceProducerBackPressureStrategy {
    var lowWatermark: Int
    var highWatermark: Int
    var currentTarget: Int

    init(minimum: Int = 1, maximum: Int = 16_384, target: Int = 256) {
        self.lowWatermark = minimum
        self.highWatermark = maximum
        self.currentTarget = target
    }

    mutating func didYield(bufferDepth: Int) -> Bool {
        // Shrink target if buffer too deep
        if bufferDepth > self.currentTarget * 2 {
            self.currentTarget = max(self.lowWatermark, self.currentTarget / 2)
        }
        return bufferDepth < self.currentTarget
    }

    mutating func didConsume(bufferDepth: Int) -> Bool {
        // Grow target if buffer drains completely
        if bufferDepth == 0 {
            self.currentTarget = min(self.highWatermark, self.currentTarget * 2)
        }
        return bufferDepth < self.currentTarget
    }
}
```

## Wire Protocol Message Encoding

Encode frontend messages with message ID and length prefix:

```swift
struct PostgresFrontendMessageEncoder: MessageToByteEncoder {
    typealias OutboundIn = PostgresFrontendMessage

    enum State {
        case flushed
        case writable
    }

    private var state: State = .flushed

    mutating func encode(data: PostgresFrontendMessage, out: inout ByteBuffer) throws {
        if self.state == .flushed {
            out.clear()
            self.state = .writable
        }

        switch data {
        case .startup(let startup):
            self.encodeStartup(startup, into: &out)
        case .parse(let parse):
            self.encodeWithMessageID(.parse, into: &out) {
                self.encodeParse(parse, into: &$0)
            }
        case .bind(let bind):
            self.encodeWithMessageID(.bind, into: &out) {
                self.encodeBind(bind, into: &$0)
            }
        // ... other message types
        }
    }

    private mutating func encodeWithMessageID(
        _ id: MessageID,
        into buffer: inout ByteBuffer,
        _ encodePayload: (inout ByteBuffer) -> ()
    ) {
        // Write message identifier
        buffer.writeInteger(id.rawValue)

        // Write placeholder for length
        let lengthIndex = buffer.writerIndex
        buffer.writeInteger(Int32(0))

        // Encode payload
        let startIndex = buffer.writerIndex
        encodePayload(&buffer)

        // Backfill length (includes 4 bytes for length field itself)
        let length = Int32(buffer.writerIndex - startIndex + 4)
        buffer.setInteger(length, at: lengthIndex)
    }

    private func encodeParse(_ parse: Parse, into buffer: inout ByteBuffer) {
        buffer.writeNullTerminatedString(parse.preparedStatementName)
        buffer.writeNullTerminatedString(parse.query)
        buffer.writeInteger(Int16(parse.parameters.count))
        for parameter in parse.parameters {
            buffer.writeInteger(parameter.rawValue)
        }
    }

    private func encodeBind(_ bind: Bind, into buffer: inout ByteBuffer) {
        buffer.writeNullTerminatedString(bind.portalName)
        buffer.writeNullTerminatedString(bind.preparedStatementName)

        // Parameter format codes
        buffer.writeInteger(Int16(bind.parameterFormats.count))
        for format in bind.parameterFormats {
            buffer.writeInteger(format.rawValue)
        }

        // Parameter values
        buffer.writeInteger(Int16(bind.parameters.count))
        for parameter in bind.parameters {
            if let value = parameter {
                buffer.writeInteger(Int32(value.readableBytes))
                buffer.writeImmutableBuffer(value)
            } else {
                buffer.writeInteger(Int32(-1)) // NULL
            }
        }

        // Result format codes
        buffer.writeInteger(Int16(bind.resultFormats.count))
        for format in bind.resultFormats {
            buffer.writeInteger(format.rawValue)
        }
    }
}

enum MessageID: UInt8 {
    case bind = 66        // 'B'
    case close = 67       // 'C'
    case describe = 68    // 'D'
    case execute = 69     // 'E'
    case flush = 72       // 'H'
    case parse = 80       // 'P'
    case sync = 83        // 'S'
    case terminate = 88   // 'X'
    case password = 112   // 'p'
    case copyDone = 99    // 'c'
    case copyData = 100   // 'd'
    case copyFail = 102   // 'f'
}
```

## Cell-Based Data Access

Wrap column values with rich metadata:

```swift
public struct PostgresCell: Sendable, Equatable {
    public let bytes: ByteBuffer?
    public let dataType: PostgresDataType
    public let format: PostgresFormat
    public let columnName: String
    public let columnIndex: Int

    @inlinable
    public func decode<T: PostgresDecodable>(
        _ type: T.Type,
        context: PostgresDecodingContext<some PostgresJSONDecoder> = .default,
        file: String = #fileID,
        line: Int = #line
    ) throws -> T {
        do {
            return try T._decodeRaw(
                from: self.bytes,
                type: self.dataType,
                format: self.format,
                context: context
            )
        } catch let error as PostgresDecodingError {
            // Enrich error with cell context
            throw PostgresDecodingError.withContext(
                error,
                columnName: self.columnName,
                columnIndex: self.columnIndex,
                file: file,
                line: line
            )
        }
    }
}

public struct PostgresRow: Sendable {
    let data: DataRow
    let lookupTable: [String: Int]
    let columns: [RowDescription.Column]

    // Access by column name
    public subscript(column: String) -> PostgresCell {
        guard let index = self.lookupTable[column] else {
            fatalError("Column '\(column)' not found in row")
        }
        return self[column: index]
    }

    // Access by index
    public subscript(column index: Int) -> PostgresCell {
        PostgresCell(
            bytes: self.data[index],
            dataType: self.columns[index].dataType,
            format: self.columns[index].format,
            columnName: self.columns[index].name,
            columnIndex: index
        )
    }
}
```

## Configuration with Nested Types

Organize configuration with clear hierarchy:

```swift
extension PostgresConnection {
    public struct Configuration: Sendable {
        public enum EndpointInfo: Sendable {
            case connectToServer(host: String, port: Int)
            case configureChannel((any Channel) async throws -> ())
            case bindUnixDomainSocket(path: String)
        }

        public struct TLS: Sendable {
            enum Mode {
                case disable
                case prefer(TLSConfiguration)
                case require(TLSConfiguration)
            }

            var mode: Mode

            public static var disable: Self { .init(mode: .disable) }

            public static func prefer(_ configuration: TLSConfiguration) -> Self {
                .init(mode: .prefer(configuration))
            }

            public static func require(_ configuration: TLSConfiguration) -> Self {
                .init(mode: .require(configuration))
            }
        }

        public struct Options: Sendable {
            public var connectTimeout: Duration
            public var tlsServerName: String?
            public var requireBackendKeyData: Bool

            public init(
                connectTimeout: Duration = .seconds(10),
                tlsServerName: String? = nil,
                requireBackendKeyData: Bool = true
            ) {
                self.connectTimeout = connectTimeout
                self.tlsServerName = tlsServerName
                self.requireBackendKeyData = requireBackendKeyData
            }
        }

        let endpointInfo: EndpointInfo
        public var username: String
        public var password: String?
        public var database: String?
        public var tls: TLS
        public var options: Options

        // TCP connection initializer
        public init(
            host: String,
            port: Int = 5432,
            username: String,
            password: String? = nil,
            database: String? = nil,
            tls: TLS = .prefer(.makeClientConfiguration())
        ) {
            self.endpointInfo = .connectToServer(host: host, port: port)
            self.username = username
            self.password = password
            self.database = database
            self.tls = tls
            self.options = Options()
        }

        // Unix domain socket initializer
        public init(
            unixSocketPath: String,
            username: String,
            password: String? = nil,
            database: String? = nil
        ) {
            self.endpointInfo = .bindUnixDomainSocket(path: unixSocketPath)
            self.username = username
            self.password = password
            self.database = database
            self.tls = .disable
            self.options = Options()
        }
    }
}
```

## COPY FROM Pattern for Bulk Loading

Implement efficient bulk data loading with backpressure:

```swift
extension PostgresConnection {
    public func copyFrom(
        table: String,
        columns: [String]? = nil,
        format: PostgresCopyFromFormat = .text(),
        _ writeData: @escaping @Sendable (PostgresCopyFromWriter) async throws -> ()
    ) async throws -> Int64 {
        // Build COPY query
        let query = buildCopyFromQuery(table: table, columns: columns, format: format)

        // Start COPY mode
        let writer = try await self.startCopy(query)

        do {
            // Stream data through writer
            try await writeData(writer)
            // Signal completion
            return try await writer.done()
        } catch {
            // Signal failure
            try await writer.failed(error)
            throw error
        }
    }

    private func buildCopyFromQuery(
        table: String,
        columns: [String]?,
        format: PostgresCopyFromFormat
    ) -> String {
        var query = "COPY \(escapeIdentifier(table))"

        if let columns = columns {
            query += " (\(columns.map { escapeIdentifier($0) }.joined(separator: ", ")))"
        }

        query += " FROM STDIN"

        switch format {
        case .text(let delimiter):
            if let delimiter = delimiter {
                query += " WITH (FORMAT text, DELIMITER E'\\u\(String(delimiter.value, radix: 16))')"
            }
        case .csv(let delimiter, let header):
            query += " WITH (FORMAT csv"
            if let delimiter = delimiter {
                query += ", DELIMITER E'\\u\(String(delimiter.value, radix: 16))'"
            }
            if header {
                query += ", HEADER true"
            }
            query += ")"
        case .binary:
            query += " WITH (FORMAT binary)"
        }

        return query
    }
}

public struct PostgresCopyFromWriter: Sendable {
    private let channel: any Channel
    private let state: NIOLockedValueBox<State>

    public func write(_ buffer: ByteBuffer) async throws {
        try await withCheckedThrowingContinuation { continuation in
            self.channel.write(
                PostgresFrontendMessage.copyData(buffer),
                promise: nil
            )
            continuation.resume()
        }
    }

    func done() async throws -> Int64 {
        try await withCheckedThrowingContinuation { continuation in
            self.channel.write(PostgresFrontendMessage.copyDone, promise: nil)
            self.channel.write(PostgresFrontendMessage.sync) { result in
                switch result {
                case .success:
                    // Row count returned in CommandComplete message
                    continuation.resume(returning: self.state.withLockedValue { $0.rowCount })
                case .failure(let error):
                    continuation.resume(throwing: error)
                }
            }
            self.channel.flush()
        }
    }

    func failed(_ error: Error) async throws {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            self.channel.write(
                PostgresFrontendMessage.copyFail(error.localizedDescription),
                promise: nil
            )
            self.channel.write(PostgresFrontendMessage.sync) { _ in
                continuation.resume()
            }
            self.channel.flush()
        }
    }
}
```

## Testing with Embedded Channel

Test channel handlers without network:

```swift
@Test func testPreparedStatementDeduplication() async throws {
    let eventLoop = NIOAsyncTestingEventLoop()
    let channel = try await NIOAsyncTestingChannel(loop: eventLoop) { channel in
        try channel.pipeline.syncOperations.addHandlers(
            ReverseByteToMessageHandler(PSQLFrontendMessageDecoder()),
            ReverseMessageToByteHandler(PSQLBackendMessageEncoder())
        )
    }

    // Add handler under test
    let handler = PostgresChannelHandler(configuration: config, eventLoop: eventLoop)
    try await channel.pipeline.addHandler(handler)

    // Simulate connection establishment
    try await channel.writeInbound(PostgresBackendMessage.authentication(.ok))
    try await channel.writeInbound(PostgresBackendMessage.backendKeyData(.init(processID: 1, secretKey: 2)))
    try await channel.writeInbound(PostgresBackendMessage.readyForQuery(.idle))

    // Execute two concurrent queries with same prepared statement
    async let result1 = handler.execute(prepared: MyPreparedStatement(param: "a"))
    async let result2 = handler.execute(prepared: MyPreparedStatement(param: "b"))

    // Verify only ONE parse message (deduplication)
    let message = try await channel.waitForOutboundWrite(as: PostgresFrontendMessage.self)
    guard case .parse(let parse) = message else {
        Issue.record("Expected parse message")
        return
    }
    #expect(parse.query == MyPreparedStatement.sql)

    // Write mock responses
    try await channel.writeInbound(PostgresBackendMessage.parseComplete)
    try await channel.writeInbound(PostgresBackendMessage.parameterDescription([.text]))
    try await channel.writeInbound(PostgresBackendMessage.noData)

    // Both queries should complete after statement prepared once
    let (r1, r2) = try await (result1, result2)
    #expect(r1 == r2)
}

// Environment-based test configuration
extension XCTestCase {
    static var shouldRunLongRunningTests: Bool {
        if let value = env("POSTGRES_LONG_RUNNING_TESTS") {
            return ["true", "1", "yes"].contains(value.lowercased())
        }
        return false
    }

    static var shouldRunPerformanceTests: Bool {
        if let value = env("POSTGRES_PERFORMANCE_TESTS") {
            return ["true", "1", "yes"].contains(value.lowercased())
        }
        #if DEBUG
        return false
        #else
        return true
        #endif
    }
}

func env(_ name: String) -> String? {
    getenv(name).flatMap { String(cString: $0) }
}
```

## Connection Pool Integration

Configure pool behavior from client configuration:

```swift
public final class PostgresClient: Sendable, ServiceLifecycle.Service {
    public struct Configuration: Sendable {
        public struct Options: Sendable {
            public var minimumConnections: Int
            public var maximumConnections: Int
            public var connectionIdleTimeout: Duration
            public var keepAliveBehavior: KeepAliveBehavior?

            public struct KeepAliveBehavior: Sendable {
                public var frequency: Duration
                public var query: PostgresQuery

                public init(
                    frequency: Duration = .seconds(30),
                    query: PostgresQuery = "SELECT 1"
                ) {
                    self.frequency = frequency
                    self.query = query
                }
            }
        }

        public var host: String
        public var port: Int
        public var username: String
        public var password: String?
        public var database: String?
        public var tls: PostgresConnection.Configuration.TLS
        public var options: Options

        public init(
            host: String,
            port: Int = 5432,
            username: String,
            password: String? = nil,
            database: String? = nil,
            tls: PostgresConnection.Configuration.TLS = .prefer(.makeClientConfiguration()),
            options: Options = Options()
        ) {
            // ...
        }
    }

    private let pool: ConnectionPool<PostgresConnection, ...>

    public func withConnection<Result>(
        _ closure: (PostgresConnection) async throws -> Result
    ) async throws -> Result {
        let lease = try await self.pool.leaseConnection()
        defer { lease.release() }
        return try await closure(lease.connection)
    }

    // Convenience method using pool
    public func query(
        _ query: PostgresQuery,
        logger: Logger
    ) async throws -> PostgresRowSequence {
        try await self.withConnection { connection in
            try await connection.query(query, logger: logger)
        }
    }
}

// Pool configuration mapping
extension ConnectionPoolConfiguration {
    init(_ config: PostgresClient.Configuration) {
        self.minimumConnectionCount = config.options.minimumConnections
        self.maximumConnectionSoftLimit = config.options.maximumConnections
        self.maximumConnectionHardLimit = config.options.maximumConnections
        self.idleTimeout = config.options.connectionIdleTimeout
    }
}
```
