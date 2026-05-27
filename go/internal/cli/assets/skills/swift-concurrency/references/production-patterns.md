# Production Swift Concurrency Patterns

Advanced concurrency patterns extracted from production server frameworks.

## Actor-Based Server State

### State Machine with Actor

```swift
public actor Server<ChildChannel: ServerChildChannel>: Service {
    enum State: CustomStringConvertible {
        case initial(childChannelSetup: ChildChannel, configuration: ServerConfiguration, onServerRunning: (@Sendable (any Channel) async -> Void)?)
        case starting
        case running(asyncChannel: AsyncServerChannel, quiescingHelper: ServerQuiescingHelper)
        case shuttingDown(shutdownPromise: EventLoopPromise<Void>)
        case shutdown

        var description: String {
            switch self {
            case .initial: return "initial"
            case .starting: return "starting"
            case .running: return "running"
            case .shuttingDown: return "shuttingDown"
            case .shutdown: return "shutdown"
            }
        }
    }

    var state: State {
        didSet { self.logger.trace("Server State: \(self.state)") }
    }

    // Explicitly non-isolated for logging from any context
    nonisolated let logger: Logger

    public func run() async throws {
        switch self.state {
        case .initial(let setup, let config, let callback):
            self.state = .starting
            // Proceed with setup
        case .starting, .running, .shuttingDown, .shutdown:
            throw ServerError.invalidState
        }
    }
}
```

**Benefits:**
- Actor isolation prevents data races
- Explicit state transitions visible in code
- `nonisolated` for values that don't need protection

## Noncopyable State Machine

### ~Copyable for Explicit Ownership

```swift
struct StateMachine: ~Copyable {
    struct ActiveState {
        var openStreams: Set<HTTP2StreamID> = []
    }

    struct ClosingState {
        var pendingStreams: Set<HTTP2StreamID>
    }

    enum State {
        case active(ActiveState)
        case closing(ClosingState)
        case closed
    }

    private var state: State

    // `consume` pattern ensures state is fully handled
    mutating func streamOpened(_ id: HTTP2StreamID) {
        switch consume self.state {
        case .active(var activeState):
            activeState.openStreams.insert(id)
            self.state = .active(activeState)
        case .closing, .closed:
            fatalError("Cannot open stream in non-active state")
        }
    }

    mutating func initiateGracefulShutdown() -> [HTTP2StreamID] {
        switch consume self.state {
        case .active(let activeState):
            let streams = Array(activeState.openStreams)
            self.state = .closing(ClosingState(pendingStreams: activeState.openStreams))
            return streams
        case .closing(let closingState):
            return Array(closingState.pendingStreams)
        case .closed:
            return []
        }
    }
}
```

**Benefits:**
- `~Copyable` prevents accidental duplication
- `consume` pattern prevents accessing old state
- Compile-time enforcement of state handling

## Structured Concurrency Patterns

### withDiscardingTaskGroup for Fire-and-Forget

```swift
await withDiscardingTaskGroup { group in
    for try await client in inbound {
        group.addTask {
            await handleClient(client)
        }
    }
}
```

Use `withDiscardingTaskGroup` (not `withTaskGroup`) when you don't need to collect results. This:
- Automatically handles task completion
- Avoids memory accumulation from unused results
- Still provides structured concurrency guarantees

### Graceful Shutdown Handler

```swift
await withGracefulShutdownHandler {
    // Main work
    await withDiscardingTaskGroup { group in
        do {
            try await asyncChannel.executeThenClose { inbound in
                for try await childChannel in inbound {
                    group.addTask {
                        await handleChild(childChannel)
                    }
                }
            }
        } catch {
            logger.error("Error: \(error)")
        }
    }
} onGracefulShutdown: {
    // Shutdown handler - spawn a Task to avoid blocking signal handler
    Task {
        do {
            try await self.shutdownGracefully()
        } catch {
            self.logger.error("Shutdown error: \(error)")
        }
    }
}
```

**Key points:**
- `onGracefulShutdown` is synchronous - use `Task` for async work
- Structured concurrency ensures child tasks complete before shutdown
- Clean resource cleanup guaranteed

### executeThenClose Pattern

```swift
try await channel.executeThenClose { inbound, outbound in
    // Work with the channel
    for try await message in inbound {
        try await outbound.write(response)
    }
}
// Channel is guaranteed closed here, even if an error occurred
```

Use `executeThenClose` for resources that need cleanup. It:
- Guarantees cleanup happens
- Works with both success and error paths
- Follows structured concurrency principles

## UnsafeTransfer for Sendable Boundaries

### When You Know It's Safe

```swift
@usableFromInline
package struct UnsafeTransfer<Wrapped> {
    @usableFromInline
    package var wrappedValue: Wrapped

    @inlinable
    package init(_ wrappedValue: Wrapped) {
        self.wrappedValue = wrappedValue
    }
}

extension UnsafeTransfer: @unchecked Sendable {}

// Usage: wrap non-Sendable value you know is safe to transfer
let handler = UnsafeTransfer(nonSendableHandler)
Task {
    // Access the wrapped value
    handler.wrappedValue.handle()
}
```

**When to use:**
- Capturing non-Sendable values you control
- You guarantee no concurrent access
- Document why it's safe

**Prefer alternatives:**
- Making the type Sendable
- Using actors
- Restructuring to avoid crossing boundaries

## Lock-Free Concurrency

### Atomic Operations with ManagedAtomic

```swift
final class DateCache: Service {
    // Container class for atomic reference
    final class DateContainer: AtomicReference, Sendable {
        let date: String
        init(date: String) { self.date = date }
    }

    let dateContainer: ManagedAtomic<DateContainer>

    init() {
        self.dateContainer = .init(.init(date: Date.now.httpHeader))
    }

    // Background service updates the cache
    public func run() async throws {
        let timerSequence = AsyncTimerSequence(interval: .seconds(1), clock: .suspending)
            .cancelOnGracefulShutdown()
        for try await _ in timerSequence {
            // Atomic store with release semantics
            self.dateContainer.store(.init(date: Date.now.httpHeader), ordering: .releasing)
        }
    }

    // Fast atomic read for every request
    public var date: String {
        // Atomic load with acquire semantics
        self.dateContainer.load(ordering: .acquiring).date
    }
}
```

**Memory orderings:**
- `releasing`: All prior writes visible before this store
- `acquiring`: Sees all writes from corresponding release
- `relaxed`: No ordering guarantees (for counters/flags)

## Service Lifecycle Integration

### Application as Service

```swift
public protocol ApplicationProtocol: Service {
    associatedtype Responder: HTTPResponder

    var responder: Responder { get async throws }
    var server: HTTPServerBuilder { get }
    var services: [any Service] { get }
}

extension ApplicationProtocol {
    public func run() async throws {
        let dateCache = DateCache()
        let serverService = try await buildServer(dateCache: dateCache)

        let services: [any Service] = self.services + [dateCache, serverService]
        let serviceGroup = ServiceGroup(
            configuration: .init(services: services, logger: self.logger)
        )

        try await serviceGroup.run()
    }
}
```

**Benefits:**
- Standard service lifecycle management
- Automatic graceful shutdown
- Proper signal handling (SIGTERM, SIGINT)
- Resource cleanup guarantees

## Sendable Closure Patterns

### @Sendable Requirement

```swift
public struct Router<Context: RequestContext> {
    public func get<Output: ResponseGenerator>(
        _ path: String,
        handler: @escaping @Sendable (Request, Context) async throws -> Output
    ) {
        // Handler must be @Sendable because it's called from different isolation domains
    }
}

// Usage
router.get("/users") { request, context in
    // This closure is @Sendable - captured values must be Sendable
    let users = try await database.fetchUsers()
    return users
}
```

### Capture Lists for Sendable Closures

```swift
// Bad - captures mutable state
var counter = 0
router.get("/count") { _, _ in
    counter += 1  // Error: cannot capture mutable variable in @Sendable closure
    return "\(counter)"
}

// Good - capture immutable snapshot
let counterService = CounterService()
router.get("/count") { _, _ in
    let count = await counterService.increment()
    return "\(count)"
}
```

## Nonisolated Access

### nonisolated for Safe Properties

```swift
public actor Server {
    // These don't need actor protection - immutable after init
    nonisolated let logger: Logger
    nonisolated let configuration: ServerConfiguration

    // This needs actor protection - mutable
    var state: State

    // nonisolated method for properties that don't need protection
    nonisolated public var serverName: String {
        configuration.serverName
    }
}
```

**Use `nonisolated` when:**
- Property is immutable (let)
- Property is Sendable and doesn't interact with actor state
- Method only reads immutable configuration

## AsyncStream for Resource-Bound Iteration

### Producing Values from Non-Async Sources

```swift
public struct ClientIterator: AsyncIteratorProtocol {
    var continuation: AsyncStream<Client>.Continuation
    var iterator: AsyncStream<Client>.AsyncIterator

    init(server: Server) {
        let stream = AsyncStream { continuation in
            // Setup callback-based producer
            server.onClientConnect = { client in
                continuation.yield(client)
            }
            server.onClose = {
                continuation.finish()
            }
        }
        self.iterator = stream.makeAsyncIterator()
    }

    mutating func next() async -> Client? {
        await iterator.next()
    }
}
```

**AsyncStream use cases:**
- Bridging callback-based APIs to async/await
- Converting delegate patterns to AsyncSequence
- Rate limiting with buffering
