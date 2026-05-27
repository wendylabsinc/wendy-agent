# Async Sequences and Streams

Patterns for iterating over values that arrive over time.

## AsyncSequence

Protocol for asynchronous iteration over values that become available over time.

### Basic usage

```swift
for await value in someAsyncSequence {
    print(value)
}
```

**Key difference from Sequence**: Values may not all be available immediately.

### Custom implementation

```swift
struct Counter: AsyncSequence {
    typealias Element = Int
    
    let limit: Int

    struct AsyncIterator: AsyncIteratorProtocol {
        let limit: Int
        var current = 1
        mutating func next() async -> Int? {
            guard !Task.isCancelled else { return nil }
            guard current <= limit else { return nil }
            return current
        }
    }

    func makeAsyncIterator() -> AsyncIterator {
        AsyncIterator(limit: limit)
    }
}

// Usage
for await count in Counter(limit: 5) {
    print(count) // 1, 2, 3, 4, 5
}
```

### Standard operators

Same functional operators as regular sequences:

```swift
// Filter
for await even in Counter(limit: 5).filter({ $0 % 2 == 0 }) {
    print(even) // 2, 4
}

// Map
let mapped = Counter(limit: 5).map { $0 % 2 == 0 ? "Even" : "Odd" }
for await label in mapped {
    print(label)
}

// Contains (awaits until found or sequence ends)
let contains = await Counter(limit: 5).contains(3) // true
```

### Termination

Return `nil` from `next()` to end iteration:

```swift
mutating func next() async -> Int? {
    guard !Task.isCancelled else {
        return nil // Stop on cancellation
    }
    
    guard current <= limit else {
        return nil // Stop at limit
    }
    
    return current
}
```

Prefer referring to AsyncSequences as `some AsyncSequence<Element, Error>`, rather than their concrete types. This erases the concrete type, while retaining the type safety and specialization.

## AsyncStream

Convenient way to create async sequences without implementing protocols.

### Basic creation

```swift
func withNumbers(_ handle: @escaping @Sendable (some AsyncSequence<Int, Never>) -> Void) async {
    let (stream, continuation) = AsyncStream<Int>.makeStream()
    await withTaskGroup(of: Void.self) { group in
        group.addTask {
            await handle(stream)
        }

        for i in 1...5 {
            continuation.yield(i)
        }
        continuation.finish()
    }
}

await withNumbers { stream in
    for await value in stream {
        print(value)
    }
}
```

### AsyncThrowingStream with unfolding

For streams that can fail:

```swift
let throwingStream = AsyncThrowingStream<Int, any Error> { continuation in
    continuation.yield(1)
    continuation.yield(2)
    continuation.finish(throwing: SomeError())
}

do {
    for try await value in throwingStream {
        print(value)
    }
} catch {
    print("Error: \(error)")
}
```

## Bridging Closures to Streams

### Progress + completion handlers

```swift
// Old closure-based API
struct FileDownloader {
    enum Status {
        case downloading(Float)
        case finished(Data)
    }
    
    func download(
        _ url: URL,
        progressHandler: @escaping (Float) -> Void,
        completion: @escaping (Result<Data, Error>) -> Void
    ) throws {
        // Implementation
    }
}

// Modern stream-based API
extension FileDownloader {
    func download(_ url: URL) -> AsyncThrowingStream<Status, Error> {
        AsyncThrowingStream { continuation in
            do {
                try self.download(url, progressHandler: { progress in
                    continuation.yield(.downloading(progress))
                }, completion: { result in
                    switch result {
                    case .success(let data):
                        continuation.yield(.finished(data))
                        continuation.finish()
                    case .failure(let error):
                        continuation.finish(throwing: error)
                    }
                })
            } catch {
                continuation.finish(throwing: error)
            }
        }
    }
}

// Usage
for try await status in downloader.download(url) {
    switch status {
    case .downloading(let progress):
        print("Progress: \(progress)")
    case .finished(let data):
        print("Done: \(data.count) bytes")
    }
}
```

### Simplified with Result

```swift
AsyncThrowingStream { continuation in
    try self.download(url, progressHandler: { progress in
        continuation.yield(.downloading(progress))
    }, completion: { result in
        continuation.yield(with: result.map { .finished($0) })
        continuation.finish()
    })
}
```

## Stream Lifecycle

Streams cancel when:
- Enclosing task cancels
- Stream goes out of scope

```swift
try await withTaskGroup(of: Void.self) { group in
    group.addTask {
        for try await status in download(url) {
            print(status)
        }
    }

    group.cancelAll() // Triggers onTermination with .cancelled
}
```

**No explicit cancel method** - rely on task cancellation.

## Buffer Policies

Control what happens to values when no one is awaiting:

### .unbounded (default)

Buffers all values until consumed, which may indefinitely grow. Both the following examples are equivalent:

```swift
let (stream, continuation) = AsyncStream<Int>.makeStream(bufferingPolicy: .unbounded)
let (stream, continuation) = AsyncStream<Int>.makeStream()
```

Note: Unbounded streams are not suitable for long-running operations, as they will keep all values in memory until consumed.
This may cause memory leaks, therefore backpressure is strongly recommended over unbounded streams.

### .bufferingNewest(n)

Keeps only the newest N values:

```swift
let (stream, continuation) = AsyncStream<Int>.makeStream(bufferingPolicy: .bufferingNewest(1))
continuation.yield(1) // Received
continuation.yield(2) // Discarded
continuation.finish()
```

### .bufferingOldest(n)

Keeps only the oldest N values:

```swift
let (stream, continuation) = AsyncStream<Int>.makeStream(bufferingPolicy: .bufferingOldest(1))
continuation.yield(1) // Received
continuation.yield(2) // Discarded
continuation.finish()
```

**Use case**: Location updates, file system changes - only care about latest.

## Task groups

```swift
await withTaskGroup(of: Image.self) { group in
    for url in urls {
        group.addTask { await download(url) }
    }
    
    for await image in group {
        display(image)
    }
}
```

### Example Progress reporting

```swift
func download(_ url: URL) -> AsyncThrowingStream<DownloadEvent, Error> {
    AsyncThrowingStream { continuation in
        Task {
            // ❌ Unstructured task leads to side effects
            var progress: Double = 0
            while progress < 1.0 {
                progress += 0.1
                progressHandler(progress)
                try await Task.sleep(for: .milliseconds(100))
            }
            
            _ = try await URLSession.shared.data(from: url).0
        }
    }
}

// ✅ No side effects, simpler code, backpressure
func withDownloadProgress(url: URL, progressHandler: @Sendable @escaping (Double) -> Void) async throws -> Data {
    var progress: Double = 0
    while progress < 1.0 {
        progress += 0.1
        progressHandler(progress)
        try await Task.sleep(for: .milliseconds(100))
    }
    
    return try await URLSession.shared.data(from: url).0
}
```