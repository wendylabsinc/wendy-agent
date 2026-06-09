# EventLoops in SwiftNIO

> Reference: [SwiftNIO Fundamentals](https://swiftonserver.com/using-swiftnio-fundamentals/)

## What is an EventLoop?

An **EventLoop** is literally what its name suggests: a `while` loop that polls for various types of events. It's the core concept behind SwiftNIO's event-driven architecture.

EventLoops are central to how SwiftNIO handles nonblocking I/O efficiently. Rather than blocking execution while waiting for network data, the EventLoop registers interest in I/O events and wakes up only when there's work to do.

## How EventLoops Work

### The Polling Loop

An EventLoop generally runs on its own thread. On that thread, it runs a `while` loop that:

1. Polls for events (using platform-specific APIs like `epoll`, `kqueue` or `WSAPoll`)
2. When events are received, triggers functions that read or write data to sockets
3. When all I/O operations are handled, blocks execution until a new event is received

This blocking is efficient because:
- The function wakes up immediately when the next event happens
- It doesn't waste CPU time spinning in circles waiting for events

### Reading with EventLoops

When a file descriptor is set to nonblocking mode:
- Read operations return any data that's available immediately
- If no data is available, the read returns an error
- The EventLoop registers the file descriptor for notification when new data arrives
- The EventLoop continues working on other file descriptors or application logic
- When new data is available, execution resumes

## Critical Rule: Never Block the EventLoop

**Blocking the EventLoop is one of the most common mistakes in SwiftNIO applications.**

### Why Blocking is Bad

The EventLoop is a **shared resource**. If your application does heavy work on the EventLoop without yielding:

- Other file descriptors cannot receive or send data
- In a web server, one request blocks all other requests
- Database drivers cannot receive query results
- **Deadlocks** can occur if you're waiting for a result that can only arrive via the blocked EventLoop

### What Constitutes Blocking

- Synchronous file I/O operations
- CPU-intensive computations
- Synchronous network calls
- Thread-blocking sleep operations
- Waiting on locks held by other EventLoop work

### Best Practices

1. **Offload heavy work** to a separate thread pool or dispatch queue
2. **Use async/await** with Swift Concurrency for long-running operations
3. **Keep EventLoop callbacks short** - do minimal processing and return quickly
4. **Use `EventLoop.execute`** to schedule work back onto the EventLoop when needed

## EventLoops and Swift Concurrency

When using Swift Concurrency with SwiftNIO:

- Prefer `async`/`await` over `EventLoopFuture` chains where possible
- Use `NIOAsyncChannel` for modern async/await-based channel handling
- Remember that `await` points are suspension points - they don't block the EventLoop

## Platform-Specific Implementations

SwiftNIO uses different underlying mechanisms depending on the platform:

| Platform | Mechanism |
|----------|-----------|
| Linux | `epoll` |
| macOS/iOS | `kqueue` |
| Windows | `WSAPoll` |

These are abstracted away by the `EventLoop` API, so your code works the same across platforms.

## Common Patterns

### Getting the Current EventLoop

```swift
// From a Channel
let eventLoop = channel.eventLoop

// From an EventLoopGroup
let eventLoop = eventLoopGroup.any() // Returns any event loop from the group
```

The preferred EventLoopGroup is `MultiThreadedEventLoopGroup.singleton`. This is a singleon with one event loop per available CPU core.

### Scheduling Work

```swift
// Schedule synchronous work on the EventLoop
eventLoop.execute {
    // Work here
}

// Schedule synchronous work on the EventLoop
eventLoop.scheduleTask(deadline: .now() + .seconds(5)) {
    // Delayed work
}
```

### Creating Promises and Futures

```swift
let promise = eventLoop.makePromise(of: String.self)
let future = promise.futureResult

// Complete the promise
promise.succeed("Success!")
// or
promise.fail(MyError.somethingWentWrong)
```

Futures and promises should only be used in low-level networking code, like protocol implementations. Higher-level code should use swift concurrency.