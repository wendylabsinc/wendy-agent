# Swift Library Performance Patterns

High-performance patterns for Swift library development.

## Inlining Annotations

### Strategic Use of @inlinable and @usableFromInline

```swift
public struct Router<Context: RequestContext> {
    // @usableFromInline exposes to other modules for inlining
    @usableFromInline
    internal var trie: TrieBuilder<Handler<Context>>

    // @inlinable allows cross-module inlining of hot paths
    @inlinable
    public func buildResponder() -> some Responder<Context> {
        let trie = self.trie.build()
        return RouterResponder(trie: trie)
    }
}

// Internal types need @usableFromInline to be used in @inlinable functions
@usableFromInline
struct RouterResponder<Context: RequestContext>: Responder {
    @usableFromInline
    let trie: Trie<Handler<Context>>

    @inlinable
    func respond(to request: Request, context: Context) async throws -> Response {
        // Hot path - inlined across module boundaries
    }
}
```

**When to use:**
- `@inlinable`: Public functions that are performance-critical hot paths
- `@usableFromInline`: Internal types/functions used by `@inlinable` code
- Avoid overusing: increases binary size and limits future changes (ABI)

## Noncopyable Types

### Move-Only Types for Resource Management

```swift
// ~Copyable ensures single use
public struct ResponseWriter: ~Copyable {
    @usableFromInline
    let outbound: AsyncChannelWriter<ResponsePart>

    // `consuming` takes ownership - can't use ResponseWriter after this
    @inlinable
    public consuming func writeHead(_ head: HTTPResponse) async throws -> some BodyWriter {
        try await self.outbound.write(.head(head))
        return BodyWriter(outbound: self.outbound)
    }
}
```

**Benefits:**
- Prevents accidental double-writes at compile time
- Enables zero-copy optimizations
- Makes ownership semantics explicit

### Low-Level Buffer Management

```swift
@usableFromInline
struct OutputBuffer: ~Copyable {
    @usableFromInline
    var bytes: UnsafeMutableBufferPointer<UInt8>
    @usableFromInline
    var count: Int

    @inlinable
    init(capacity: Int) {
        self.bytes = .allocate(capacity: capacity)
        self.count = 0
    }

    @inlinable
    deinit {
        bytes.deallocate()
    }

    @inlinable
    mutating func append(_ byte: UInt8) {
        bytes[count] = byte
        count += 1
    }
}
```

**Benefits:**
- Manual memory management when needed
- Compile-time safety with ~Copyable
- Prevents double-frees and leaks
- Efficient for hot paths

### Span-Based APIs with `_read`/`_modify` Accessors

For types wrapping buffers, expose spans via computed properties using `_read` and `_modify` accessors. Requires the `LifetimeDependence` experimental feature in Package.swift.

```swift
// Package.swift
swiftSettings: [
    .enableExperimentalFeature("LifetimeDependence"),
]

// Buffer type with span-based access
public struct Buffer: @unchecked Sendable {
    private final class Storage: @unchecked Sendable {
        var data: UnsafeMutablePointer<UInt8>
        let size: Int

        func copy() -> Storage { /* deep copy */ }
        deinit { data.deallocate() }
    }

    private var storage: Storage

    // Read-only span access via _read accessor
    public var bytes: RawSpan {
        _read {
            yield RawSpan(_unsafeStart: storage.data, byteCount: storage.size)
        }
    }

    // Mutable span access with Copy-on-Write
    public var mutableBytes: MutableRawSpan {
        _read {
            fatalError("Cannot read mutableBytes")
        }
        _modify {
            // Ensure unique ownership before write (CoW)
            if !isKnownUniquelyReferenced(&storage) {
                storage = storage.copy()
            }
            var span = MutableRawSpan(_unsafeStart: storage.data, byteCount: storage.size)
            yield &span
        }
    }
}
```

**Benefits:**
- Zero-copy access to underlying memory
- Lifetime-bound spans cannot escape their scope
- CoW semantics for mutable access
- Natural property syntax: `buffer.bytes[0]`

**Known Issue (Swift 6.2.3):** Compiler may crash in LifetimeDependenceScopeFixup when chaining span methods. Provide separate closure-based methods for C interop as fallback.

## Data Structures for Libraries

### Trie for O(path length) Lookup

Routes/paths organized in a trie for efficient matching:

```swift
@usableFromInline
enum TrieToken: Equatable, Sendable {
    case null
    case literal(constantIndex: UInt32)
    case capture(parameterIndex: UInt32)
    case wildcard
    case recursiveWildcard
    case deadEnd
}

@usableFromInline
struct TrieNode: Sendable {
    @usableFromInline let valueIndex: Int
    @usableFromInline let token: TrieToken
    @usableFromInline var nextSiblingNodeIndex: Int
}

@usableFromInline
struct Trie<Value>: Sendable {
    @usableFromInline var nodes = [TrieNode]()
    @usableFromInline var stringValues = [Substring]()
}
```

**Benefits:**
- O(path segments) lookup instead of O(routes)
- Low memory overhead with compact arrays
- Cache-friendly linear data structure
- Deterministic performance regardless of entry count

### FlatDictionary for Small Collections

Custom dictionary optimized for small collections (like HTTP headers):

```swift
public struct FlatDictionary<Key: Hashable, Value>: Collection {
    private var elements: [(key: Key, value: Value)]
    private var hashKeys: [Int]

    // O(n) lookup, but n is small and cache-friendly
    public subscript(_ key: Key) -> Value? {
        get {
            let hashKey = key.hashValue
            if let index = hashKeys.firstIndex(of: hashKey) {
                return elements[index].value
            }
            return nil
        }
        set {
            let hashKey = key.hashValue
            if let index = hashKeys.firstIndex(of: hashKey) {
                if let newValue {
                    elements[index].value = newValue
                } else {
                    elements.remove(at: index)
                    hashKeys.remove(at: index)
                }
            } else if let newValue {
                elements.append((key: key, value: newValue))
                hashKeys.append(hashKey)
            }
        }
    }

    // Support duplicate keys (useful for HTTP headers like Set-Cookie)
    public func getAll(_ key: Key) -> [Value] {
        let hashKey = key.hashValue
        return zip(hashKeys, elements)
            .filter { $0.0 == hashKey }
            .map { $0.1.value }
    }
}
```

**Benefits:**
- Lower overhead than Dictionary for small n
- Support for duplicate keys
- Direct iteration without hashing overhead
- Cache-friendly contiguous memory

## Lock-Free Patterns

### Atomic Reference for Cached Data

```swift
final class DateCache: Service {
    final class DateContainer: AtomicReference, Sendable {
        let date: String
        init(date: String) { self.date = date }
    }

    let dateContainer: ManagedAtomic<DateContainer>

    init() {
        self.dateContainer = .init(.init(date: Date.now.formatted))
    }

    // Background service updates periodically
    public func run() async throws {
        let timer = AsyncTimerSequence(interval: .seconds(1), clock: .suspending)
            .cancelOnGracefulShutdown()
        for try await _ in timer {
            // Atomic store - no locks needed
            self.dateContainer.store(.init(date: Date.now.formatted), ordering: .releasing)
        }
    }

    // Fast atomic read
    public var date: String {
        self.dateContainer.load(ordering: .acquiring).date
    }
}
```

**Benefits:**
- Zero-lock concurrent access
- Minimal allocations
- Correct memory ordering
- Graceful shutdown integration

### Memory Ordering Guidelines

```swift
// Write side: releasing ensures all writes are visible
container.store(newValue, ordering: .releasing)

// Read side: acquiring ensures we see all writes from store
let value = container.load(ordering: .acquiring)

// For simple flags, relaxed may be sufficient
let isRunning = runningFlag.load(ordering: .relaxed)
```

**Guidelines:**
- `acquiring`/`releasing`: For synchronizing data access
- `sequentiallyConsistent`: When ordering with other atomics matters
- `relaxed`: Only for statistics/flags where ordering doesn't matter

## State Machine Pattern

### Noncopyable State Machine

```swift
struct StateMachine: ~Copyable {
    enum State {
        case active(ActiveState)
        case closing(ClosingState)
        case closed
    }

    private var state: State

    // Consuming pattern ensures state is handled
    mutating func streamOpened(_ id: StreamID) {
        switch consume self.state {
        case .active(var activeState):
            activeState.openStreams.insert(id)
            self.state = .active(activeState)
        case .closing, .closed:
            fatalError("Cannot open stream in \(self.state)")
        }
    }

    mutating func initiateGracefulShutdown() -> [StreamID] {
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
- Explicit state transitions visible in code
- `consume` pattern prevents accessing old state
- Compile-time prevention of invalid states
- Clear lifecycle management

## Async Channel Integration

### Efficient Channel Handling with Structured Concurrency

```swift
public func handle(value: Value, logger: Logger) async {
    do {
        try await value.executeThenClose { inbound, outbound in
            await withDiscardingTaskGroup { group in
                let writer = ResponseWriter(outbound: outbound)
                for try await part in inbound {
                    switch part {
                    case .head(let head):
                        // Process head
                    case .body(let buffer):
                        // Accumulate body
                    case .end:
                        group.addTask {
                            await self.handleRequest(writer)
                        }
                    }
                }
            }
        }
    } catch {
        logger.trace("Connection error: \(error)")
    }
}
```

**Benefits:**
- `executeThenClose` guarantees cleanup
- `withDiscardingTaskGroup` for fire-and-forget handling
- No task leaks due to structured concurrency
- Efficient streaming handling
