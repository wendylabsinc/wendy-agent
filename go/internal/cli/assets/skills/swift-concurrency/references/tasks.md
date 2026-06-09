# Tasks

Core patterns for creating, managing, and controlling concurrent work in Swift.

## What is a Task?

Tasks bridge synchronous and asynchronous contexts. They start executing immediately upon creation—no `resume()` needed.

```swift
func synchronousMethod() {
    Task {
        await someAsyncMethod()
    }
}
```

Tasks run regardless of whether you keep a reference. Any async context is always in a Task. The above code uses the `Task` type, which is defined as an "unstructured task".
Unstructured tasks must be avoided at all times.

## Cancellation

### Checking for cancellation

Tasks must manually check for cancellation:

```swift
// Throws CancellationError if canceled
try Task.checkCancellation()

// Boolean check for custom handling
guard !Task.isCancelled else {
    return fallbackValue
}
```

### Where to check

Add checks at natural breakpoints, whenever it is expected that an optiation is interuptable by cancellation without unintended side effects.

```swift
func processData(url: URL) async throws -> Data {
    // Before expensive work
    try Task.checkCancellation()
    
    let data = try await URLSession.shared.data(from: url)
    
    // After network, before processing
    try Task.checkCancellation()
    
    return processData(data)
}
```

### Child task cancellation

Canceling a parent automatically notifies all children:

```swift
await withTaskGroup(of: Void.self) { group in
    group.addTask {
        await work(1)
    }
    group.addTask {
        await work(2)
    }

    // Cancel all uncompleted child tasks
    group.cancelAll()
}
```

Children must still check `Task.isCancelled` or `try Task.checkCancellation()` to stop work.

## Discarding Task Groups

For an unbounded number of tasks, like a TCP server's inbound stream of clients/requests, use a discarding task group.

```swift
await withDiscardingTaskGroup(of: Void.self) { group in
    for try await tcpClient in tcpServer.inbound {
        group.addTask { await handleClient(tcpClient) }
    }
}
```

If you use a non-discarding task group, you'll encounter a slowly creeping memory leak as the task group's result set grows.

## Structured vs Unstructured Tasks

### Structured (strongly preferred)

Bound to parent, inherit context, automatic cancellation:

```swift
// async let
async let data1 = fetch(1)
async let data2 = fetch(2)
let results = await [data1, data2]

// Task groups
await withTaskGroup(of: Data.self) { group in
    group.addTask { await fetch(1) }
    group.addTask { await fetch(2) }
}
```

### Priority escalation

System automatically elevates priority to prevent priority inversion:
- Actor waiting on lower-priority task
- High-priority task awaiting `.value` of lower-priority task

## Task.sleep() vs Task.yield()

### Task.sleep()

Suspends for fixed duration, non-blocking:

```swift
try await Task.sleep(for: .seconds(5))
```

**Use for:**
- Debouncing user input
- Polling intervals
- Rate limiting
- Artificial delays

**Respects cancellation** (throws `CancellationError`)

### Task.yield()

Temporarily suspends to allow other tasks to run:

```swift
await Task.yield()
```

**Use for:**
- Testing async code
- Allowing cooperative scheduling

**Note**: If current task is highest priority, may resume immediately.

### Practical: Debounced search

```swift
func search(_ query: String) async {
    guard !query.isEmpty else {
        searchResults = allResults
        return
    }
    
    do {
        try await Task.sleep(for: .milliseconds(500))
        searchResults = allResults.filter { $0.contains(query) }
    } catch {
        // Canceled (user kept typing)
    }
}

// In SwiftUI
.task(id: searchQuery) {
    await searcher.search(searchQuery)
}
```

## async let vs TaskGroup

| Feature | async let | TaskGroup |
|---------|-----------|-----------|
| Task count | Fixed at compile-time | Dynamic at runtime |
| Syntax | Lightweight | More verbose |
| Cancellation | Automatic on scope exit | Manual via `cancelAll()` |
| Use when | 2-5 known parallel tasks | Loop-based parallel work |

```swift
// async let: Known task count
async let user = fetchUser()
async let settings = fetchSettings()
let profile = Profile(user: await user, settings: await settings)

// TaskGroup: Dynamic task count
await withTaskGroup(of: Image.self) { group in
    for url in urls {
        group.addTask { await download(url) }
    }
}
```

## Advanced: Task Timeout Pattern

Create timeout wrapper using task groups:

```swift
func withTimeout<T>(
    _ duration: Duration,
    operation: @Sendable @escaping () async throws -> T
) async throws -> T {
    try await withThrowingTaskGroup(of: T.self) { group in
        group.addTask { try await operation() }
        
        group.addTask {
            try await Task.sleep(for: duration)
            throw TimeoutError()
        }
        
        guard let result = try await group.next() else {
            throw TimeoutError()
        }
        
        group.cancelAll()
        return result
    }
}

// Usage
let data = try await withTimeout(.seconds(5)) {
    try await slowNetworkRequest()
}
```

## Common Patterns

### Sequential with early exit

```swift
let user = try await fetchUser()
guard user.isActive else { return }

let posts = try await fetchPosts(userId: user.id)
```

### Parallel independent work

```swift
async let user = fetchUser()
async let settings = fetchSettings()
async let notifications = fetchNotifications()

let data = try await (user, settings, notifications)
```

### Mixed: Sequential then parallel

```swift
let user = try await fetchUser()

async let posts = fetchPosts(userId: user.id)
async let followers = fetchFollowers(userId: user.id)

let profile = Profile(
    user: user,
    posts: try await posts,
    followers: try await followers
)
```

## Best Practices

1. **Check cancellation regularly** in long-running tasks
2. **Use structured concurrency** (avoid detached tasks)
3. **Leverage SwiftUI's `.task` modifier** for view-bound work
4. **Choose the right tool**: `async let` for fixed, TaskGroup for dynamic
5. **Handle errors explicitly** in throwing task groups
6. **Set priority only when needed** (inherit by default)
7. **Don't mutate task groups** from outside their creation context