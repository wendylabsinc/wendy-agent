# Swift Library API Design Patterns

Patterns for designing flexible, type-safe, and ergonomic Swift library APIs.

## Protocol-Oriented Architecture

### Core Protocol with Associated Types

Use protocols with associated types for decoupled, testable designs:

```swift
// Core protocol with associated type
public protocol Handler<Context>: Sendable {
    associatedtype Context
    @Sendable func handle(input: Input, context: Context) async throws -> Output
}

// Consumer accepts any conforming type with matching Context
public struct Router<Context: RequestContext> {
    public func register<H: Handler>(
        path: String,
        handler: H
    ) where H.Context == Context {
        // Accept any Handler with matching Context
    }
}
```

**Benefits:**
- Decouples implementations from consumers
- Enables dependency injection and testing
- Supports Swift's generic specialization for performance
- Runtime polymorphism with type safety

### Protocol Composition for Return Types

Allow handlers to return any compatible type with automatic conversion:

```swift
public protocol ResponseGenerator: Sendable {
    func response(from request: Request, context: some RequestContext) throws -> Response
}

// Extend standard types to be ResponseGenerators
extension String: ResponseGenerator {
    public func response(from request: Request, context: some RequestContext) -> Response {
        let buffer = ByteBuffer(string: self)
        return Response(
            status: .ok,
            headers: [.contentType: "text/plain; charset=utf-8"],
            body: .init(byteBuffer: buffer)
        )
    }
}

extension HTTPResponse.Status: ResponseGenerator {
    public func response(from request: Request, context: some RequestContext) -> Response {
        Response(status: self, headers: [:], body: .init())
    }
}

// Optional automatically handles nil -> 204 No Content
extension Optional: ResponseGenerator where Wrapped: ResponseGenerator {
    public func response(from request: Request, context: some RequestContext) throws -> Response {
        switch self {
        case .some(let wrapped):
            return try wrapped.response(from: request, context: context)
        case .none:
            return Response(status: .noContent)
        }
    }
}

// Arrays of Encodable types work automatically
extension Array: ResponseGenerator where Element: Encodable {}
```

**Benefits:**
- Reduces boilerplate in user code
- Type-safe conversions at compile time
- Extensible to new types
- Natural Swift patterns

## Result Builder Patterns

### Middleware/Pipeline Composition

```swift
@resultBuilder
public enum PipelineBuilder<Context> {
    // Convert a middleware directly
    public static func buildExpression<M: MiddlewareProtocol>(
        _ m: M
    ) -> M where M.Context == Context {
        m
    }

    // Convert a handler closure
    public static func buildExpression<Output: ResponseGenerator>(
        _ handler: @escaping @Sendable (Request, Context) async throws -> Output
    ) -> Handler<Output, Context> {
        .init(handler)
    }

    // Compose two middleware together
    public static func buildPartialBlock<M0: MiddlewareProtocol, M1: MiddlewareProtocol>(
        accumulated m0: M0,
        next m1: M1
    ) -> CombinedMiddleware<M0, M1> where M0.Context == M1.Context {
        CombinedMiddleware(m0, m1)
    }
}

// Usage - declarative, type-safe composition
let pipeline = PipelineBuilder(context: RequestContext.self) {
    CORSMiddleware()
    LoggingMiddleware()
    Handler { request, context in
        "Hello, world!"
    }
}
```

**Benefits:**
- Creates readable, declarative APIs
- Type-safe composition at compile time
- Integrates seamlessly with Swift syntax

## Builder Pattern for Configuration

### Static Builder Methods

```swift
public struct ServerBuilder: Sendable {
    let build: @Sendable () throws -> any ServerProtocol

    public init(_ build: @escaping @Sendable () throws -> any ServerProtocol) {
        self.build = build
    }

    public static func http1(configuration: HTTP1Config = .init()) -> ServerBuilder {
        .init {
            HTTP1Server(configuration: configuration)
        }
    }

    public static func http2(tlsConfiguration: TLSConfiguration) -> ServerBuilder {
        .init {
            HTTP2Server(tlsConfiguration: tlsConfiguration)
        }
    }
}

// Usage - fluent, type-safe configuration
let app = Application(
    router: router,
    server: .http1(configuration: .init(idleTimeout: .seconds(30)))
)
```

**Benefits:**
- Fluent configuration API
- Type safety at build time
- Composable configurations
- Clear intent from usage

## Error Handling Patterns

### Domain-Specific Error Protocol

Enable errors to carry domain-specific semantics:

```swift
public protocol HTTPResponseError: Error, Sendable {
    var status: HTTPResponse.Status { get }
    var headers: HTTPFields { get }
    func body(allocator: ByteBufferAllocator) -> ByteBuffer?
}

public struct HTTPError: Error, HTTPResponseError, Sendable {
    public var status: HTTPResponse.Status
    public var headers: HTTPFields
    public var body: String?

    public init(_ status: HTTPResponse.Status) {
        self.status = status
        self.headers = [:]
        self.body = nil
    }

    public init(_ status: HTTPResponse.Status, message: String) {
        self.status = status
        self.headers = [:]
        self.body = message
    }
}
```

### Error Augmentation in Middleware

Middleware can catch errors and augment them without changing the underlying type:

```swift
public func handle(
    _ request: Request,
    context: Context,
    next: (Request, Context) async throws -> Response
) async throws -> Response {
    do {
        var response = try await next(request, context)
        // Add headers to response
        return response
    } catch {
        // Add headers to error responses too
        var additionalHeaders = HTTPFields()
        additionalHeaders[.accessControlAllowOrigin] = origin
        throw AugmentedError(
            originalError: error,
            additionalHeaders: additionalHeaders
        )
    }
}
```

## Parameter Extraction Patterns

### Type-Safe Overloads with @_disfavoredOverload

```swift
public struct Parameters {
    // Primary overload for LosslessStringConvertible
    public func get<T: LosslessStringConvertible>(_ s: String, as: T.Type) -> T? {
        self[s].flatMap { T(String($0)) }
    }

    // Disfavored to avoid ambiguity when type conforms to both protocols
    @_disfavoredOverload
    public func get<T: RawRepresentable>(_ s: String, as: T.Type) -> T? where T.RawValue == String {
        self[s].flatMap { T(rawValue: String($0)) }
    }

    // Throwing version for required parameters
    public func require<T: LosslessStringConvertible>(_ s: String, as: T.Type) throws -> T {
        guard let param = self[s] else {
            throw ParameterError.missing(s)
        }
        guard let result = T(String(param)) else {
            throw ParameterError.conversionFailed(s, param)
        }
        return result
    }
}
```

**Benefits:**
- Flexible API supporting multiple protocols
- Type-safe extraction
- Proper error messages
- `@_disfavoredOverload` ensures correct resolution

## Pattern Matching with Custom Operators

### Custom ~= for Switch Statements

```swift
public struct PathElement: Equatable, Sendable {
    enum Value: Equatable, Sendable {
        case literal(Substring)
        case capture(Substring)
        case wildcard
        case recursiveWildcard
    }

    let value: Value

    // Custom pattern matching operator
    public static func ~= (lhs: PathElement, rhs: some StringProtocol) -> Bool {
        switch lhs.value {
        case .literal(let literal):
            return literal == rhs
        case .capture:
            return true  // Captures match anything
        case .wildcard:
            return true
        case .recursiveWildcard:
            return true
        }
    }
}

// Enables pattern matching in switch statements
switch pathElement {
case .literal("users"):
    // exact match
case .capture:
    // parameter capture like {id}
case .wildcard:
    // matches any single segment
}
```

## Service Lifecycle Integration

### Application as Service

```swift
public protocol ApplicationProtocol: Service {
    associatedtype Responder: Handler

    var responder: Responder { get async throws }
    var server: ServerBuilder { get }
    var configuration: Configuration { get }
    var logger: Logger { get }
    var services: [any Service] { get }
}

extension ApplicationProtocol {
    public func run() async throws {
        let serverService = try await buildServer()

        let services: [any Service] = self.services + [serverService]
        let serviceGroup = ServiceGroup(
            configuration: .init(services: services, logger: self.logger)
        )
        try await serviceGroup.run()
    }
}
```

**Benefits:**
- Standard service lifecycle
- Graceful shutdown handling
- Resource cleanup guarantees
- Integration with system signals (SIGTERM, SIGINT)

## Testing Patterns

### Protocol-Based Test Framework

```swift
// Abstract test client protocol
protocol TestClientProtocol: Sendable {
    func execute(
        uri: String,
        method: HTTPRequest.Method,
        headers: HTTPFields,
        body: ByteBuffer?
    ) async throws -> TestResponse
}

// Multiple implementations for different test scenarios
struct InMemoryTestClient: TestClientProtocol { ... }  // Unit testing, no network
struct LiveTestClient: TestClientProtocol { ... }      // Integration testing

// Usage in tests
@Test func testEndpoint() async throws {
    let app = buildApplication()

    // .router uses InMemoryTestClient (fast, no network)
    try await app.test(.router) { client in
        try await client.execute(uri: "/test/1", method: .get) { response in
            #expect(response.status == .ok)
        }
    }

    // .live uses LiveTestClient (real server)
    try await app.test(.live) { client in
        try await client.execute(uri: "/test/1", method: .get) { response in
            #expect(response.status == .ok)
        }
    }
}
```

**Benefits:**
- Multiple testing levels (unit, integration, end-to-end)
- Flexible client abstraction
- Protocol-based for easy mocking
- Easy to test without real network

## Extension Points Pattern

### Protocol Extensions for Cross-Cutting Functionality

```swift
// Extend standard library types to work with your framework
extension String: ResponseGenerator { ... }
extension ByteBuffer: ResponseGenerator { ... }
extension HTTPResponse.Status: ResponseGenerator { ... }
extension Optional: ResponseGenerator where Wrapped: ResponseGenerator { ... }
extension Array: ResponseGenerator where Element: Encodable { ... }

// Extend external types for convenience
extension HTTPFields {
    static func defaultHeaders(
        contentType: String,
        contentLength: Int
    ) -> HTTPFields {
        var headers = HTTPFields()
        headers[.contentType] = contentType
        headers[.contentLength] = String(contentLength)
        return headers
    }
}
```

**Benefits:**
- Open for extension, closed for modification
- No need to modify original types
- Type-safe extensions
- Composable conformances
