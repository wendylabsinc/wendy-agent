# Performance

Optimizing Swift Concurrency code for speed and efficiency.

## Core Principles

### Measurement is essential

Can't improve what you don't measure. Establish baseline before optimizing.

### Start simple, optimize later

```
Synchronous → Asynchronous → Parallel
```

Move right only when proven necessary.

### Three phases of concurrency

1. **No concurrency** - Synchronous method
2. **Suspend without parallelism** - Asynchronous method
3. **Advanced concurrency** - Parallel execution

## Common Performance Issues

### UI hangs

Too much work on main thread causes interface freezes.

### Poor parallelization

Heavy work funneled into single task instead of parallel execution.

### Actor contention

Tasks waiting on busy actor, causing unnecessary suspensions.

## Using Xcode Instruments

### Swift Concurrency template

Profile with CMD + I → Select "Swift Concurrency" template.

**Instruments included**:
- **Swift Tasks**: Track running, alive, total tasks
- **Swift Actors**: Show actor execution and queue size

### Key metrics

```
Tasks:
- Total count
- Running vs suspended
- Task states (Creating, Running, Suspended, Ending)

Actors:
- Queue size
- Execution time
- Contention points

Main Thread:
- Hangs
- Blocked time
```

### Task states

- **Creating**: Task being initialized
- **Running**: Actively executing
- **Suspended**: Waiting (at await)
- **Ending**: Completing

## Identifying Issues

### Actor contention

```swift
actor Generator {
    func generate() -> Image {
        // Heavy work
    }
}

// ❌ Sequential through actor
for _ in 0..<100 {
    let image = await generator.generate() // Queue size = 1
}
```

**Instruments shows**: Actor queue never exceeds 1, no parallelism.

## Suspension Points

### What creates suspension

Every `await` is potential suspension point:

```swift
let data = await fetchData() // May suspend
```

**Not guaranteed** - if isolation matches, may not suspend.

### Suspension surface area

Code between suspension points. Larger = harder to reason about:
- Actor invariants
- Performance
- Thread hops
- Reentrancy
- State consistency

### Goal

- Do work before crossing isolation
- Cross once
- Finish job
- Only cross again when necessary

## Reducing Suspensions

### 1. Use synchronous methods

```swift
// ❌ Unnecessary async
private func scale(_ image: CGImage) async { }

func process(_ image: CGImage) async {
    let scaled = await scale(image) // Suspension point
}

// ✅ Synchronous helper
private func scale(_ image: CGImage) { }

func process(_ image: CGImage) async {
    let scaled = scale(image) // No suspension
}
```

**Rule**: If method doesn't need to suspend, don't mark async.

### 2. Prevent actor reentrancy

```swift
// ❌ Reenters actor
actor BankAccount {
    func deposit(_ amount: Int) async {
        balance += amount
        await logTransaction() // Leaves actor
        balance += bonus // Reenters - state may have changed
    }
}

// ✅ Complete work before leaving
actor BankAccount {
    func deposit(_ amount: Int) async {
        balance += amount
        balance += bonus
        await logTransaction() // Leave after state changes
    }
}
```

### 3. Inherit isolation

```swift
// ❌ Switches isolation
@MainActor
func update() async {
    await process() // Switches away from main actor
}

// ✅ Inherits isolation
@MainActor
func update() async {
    process() // Stays on main actor (if nonisolated(nonsending))
}

nonisolated(nonsending) func process() async { }
```

### 4. Use non-suspending APIs

```swift
// ❌ May suspend
try await Task.checkCancellation()

// ✅ No suspension
if Task.isCancelled {
    return
}
```

### 5. Embrace parallelism

```swift
// ❌ Sequential
for url in urls {
    let image = await download(url)
    images.append(image)
}

// ✅ Parallel
await withTaskGroup(of: Image.self) { group in
    for url in urls {
        group.addTask { await download(url) }
    }
    for await image in group {
        images.append(image)
    }
}
```

## Analyzing Suspensions in Instruments

### View task states

1. Select Swift Tasks instrument
2. Switch to "Task States" view
3. Look for Suspended states
4. Check suspension duration

### Navigate to code

1. Click task state (Running/Suspended)
2. Open Extended Detail
3. Click related method
4. Use "Open in Source Viewer"

## Choosing Execution Style

### Decision checklist

**Use async/parallel if**:
- [ ] Blocks main actor visibly (>16ms)
- [ ] Scales with data (N items → N cost)
- [ ] Involves I/O (network, disk)
- [ ] Benefits from combining operations
- [ ] Called frequently

**2+ checks** → async/parallel justified.

### Start synchronous

```swift
// Start here
func processData(_ data: Data) -> Result {
    // Fast, in-memory work
}
```

**Only move to async if**:
- Instruments show main thread hang
- User reports sluggishness
- Work scales with input size

### When to use async

```swift
func processData(_ data: Data) async -> Result {
    // Use when:
    // - Touches persistent storage
    // - Parses large datasets
    // - Network communication
    // - Proven slow by profiling
}
```

### When to use parallel

```swift
await withTaskGroup(of: Result.self) { group in
    for item in items {
        group.addTask { await process(item) }
    }
}

// Use when:
// - Multiple independent operations
// - Time-to-first-result matters
// - Work scales with collection size
// - Proven beneficial by profiling
```

## Parallelism Costs

### Tradeoffs

**Benefits**:
- Faster completion (if CPU-bound)
- Better resource utilization
- Improved responsiveness

**Costs**:
- Increased memory pressure
- CPU scheduling overhead
- System resource saturation
- Battery drain
- Thermal impact

### When parallelism hurts

```swift
// ❌ Over-parallelization
for i in 0..<1000 {
    await withTaskGroup(of: Void.self) { group in
        group.addTask { await lightWork(i) }
    }
}
// Creates 1000 tasks for trivial work
```

**Better**: Batch work or use fewer tasks.

## UX-Driven Decisions

### Smooth animations > raw speed

```swift
// 80ms on main thread, but animation stutters
@MainActor
func process() {
    heavyWork() // Freezes UI for 1 frame
}

// 100ms total, but smooth UI
@MainActor
func process() async {
    await backgroundWork() // UI stays responsive
}
```

**Perception**: Smooth feels faster than raw speed.

### Progress indication

```swift
@MainActor
func loadItems() async {
    isLoading = true
    
    for i in 0..<100 {
        let item = await fetchItem(i)
        items.append(item)
        progress = Double(i) / 100 // Incremental updates
    }
    
    isLoading = false
}
```

Background work + progress = feels faster.

## Optimization Checklist

Before optimizing, ask:

- [ ] Have I profiled with Instruments?
- [ ] Is main thread actually blocked?
- [ ] Can this be synchronous?
- [ ] Am I over-parallelizing?
- [ ] Is actor contention the issue?
- [ ] Are suspensions necessary?
- [ ] Does UX require background work?
- [ ] Will this scale with data?

## Common Patterns

### Move heavy work to background

```swift
// Before
@MainActor
func generate() {
    for _ in 0..<100 {
        let item = heavyGeneration()
        items.append(item)
    }
}

// After
@MainActor
func generate() async {
    for _ in 0..<100 {
        let item = await backgroundGenerate()
        items.append(item)
    }
}

@concurrent
func backgroundGenerate() async -> Item {
    // Heavy work off main thread
}
```

### Parallelize independent work

```swift
// Before: Sequential
for url in urls {
    let image = await download(url)
    images.append(image)
}

// After: Parallel
await withTaskGroup(of: Image.self) { group in
    for url in urls {
        group.addTask { await download(url) }
    }
    for await image in group {
        images.append(image)
    }
}
```

### Reduce actor hops

```swift
// Before: Multiple hops
actor Store {
    func process() async {
        let a = await fetch1() // Hop 1
        let b = await fetch2() // Hop 2
        let c = await fetch3() // Hop 3
        combine(a, b, c)
    }
}

// After: Batch fetches
actor Store {
    func process() async {
        async let a = fetch1()
        async let b = fetch2()
        async let c = fetch3()
        combine(await a, await b, await c) // One hop
    }
}
```

## Best Practices

1. **Profile before optimizing** - measure baseline
2. **Start synchronous** - add async only when needed
3. **Use Instruments regularly** - catch issues early
4. **Name tasks** - easier debugging in Instruments
5. **Check suspension count** - reduce unnecessary awaits
6. **Avoid premature parallelism** - has costs
7. **Consider UX** - smooth > fast
8. **Batch actor work** - reduce contention
9. **Test on real devices** - simulators lie
10. **Monitor in production** - real usage patterns differ

## Debugging Performance

### Instruments workflow

1. Profile with Swift Concurrency template
2. Identify main thread hangs
3. Check task parallelism
4. Analyze actor queue sizes
5. Review suspension points
6. Navigate to problematic code
7. Apply optimizations
8. Re-profile to verify

### Red flags in Instruments

- Main thread blocked >16ms
- Actor queue size always 1
- High suspension count
- Tasks created but not running
- Excessive task creation (1000+)