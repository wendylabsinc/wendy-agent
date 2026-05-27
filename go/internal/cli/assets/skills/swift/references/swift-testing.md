# Swift Testing

Swift Testing is the modern testing framework for Swift, included in Swift 6 toolchains and Xcode 16. It provides an expressive, macro-based API that reduces boilerplate and runs tests in parallel by default.

## Core Concepts

### @Test Macro

Mark functions as tests using the `@Test` attribute:

```swift
import Testing

@Test func basicTest() {
    #expect(1 + 1 == 2)
}

// With custom display name
@Test("User can log in successfully")
func userLogin() async throws {
    let result = try await auth.login(user: "test", password: "secret")
    #expect(result.isSuccess)
}

// Async and throwing tests
@Test func asyncTest() async throws {
    let data = try await fetchData()
    #expect(!data.isEmpty)
}
```

## Assertions

### #expect Macro

The primary assertion macro. Accepts any boolean expression:

```swift
// Basic comparisons
#expect(value == expected)
#expect(count > 0)
#expect(array.contains(element))
#expect(string.hasPrefix("Hello"))

// With custom message
#expect(age >= 18, "User must be an adult")

// Negation
#expect(!list.isEmpty)
```

### #require Macro

Unwraps optionals or stops the test on failure. Requires `try`:

```swift
// Unwrap optional - test stops if nil
let user = try #require(users.first)
#expect(user.name == "Alice")

// Require condition - test stops if false
try #require(database.isConnected)
let results = try await database.query("SELECT * FROM users")
```

Use `#require` when subsequent code depends on the condition being true.

### Error Testing

Test that code throws expected errors:

```swift
// Expect any error
#expect(throws: (any Error).self) {
    try dangerousOperation()
}

// Expect specific error type
#expect(throws: ValidationError.self) {
    try validate(input: "")
}

// Expect specific error value
#expect(throws: NetworkError.timeout) {
    try await fetchWithTimeout()
}

// Expect no error
#expect(throws: Never.self) {
    try safeOperation()
}

// Custom error validation
#expect(performing: {
    try processOrder(quantity: -1)
}, throws: { error in
    guard let orderError = error as? OrderError else { return false }
    return orderError.code == 422
})
```

### confirmation

Verify that events occur (useful for callbacks and delegates):

```swift
// Verify event happens exactly once
await confirmation { eventOccurred in
    button.onTap = {
        eventOccurred()
    }
    button.simulateTap()
}

// Verify event happens specific number of times
await confirmation(expectedCount: 3) { called in
    for _ in 0..<3 {
        processor.process { called() }
    }
}

// Verify event never happens
await confirmation(expectedCount: 0) { shouldNotBeCalled in
    cache.onEviction = { shouldNotBeCalled() }
    cache.set("key", value: "value")
}
```

### withKnownIssue

Mark code with known problems without failing the test:

```swift
// Known bug - test passes even if code fails
withKnownIssue {
    try buggyFunction()
}

// Intermittent issue
withKnownIssue(isIntermittent: true) {
    try flakyNetworkCall()
}

// With bug reference
withKnownIssue("https://github.com/org/repo/issues/123") {
    try brokenFeature()
}
```

## Test Organization

### Test Suites

Group related tests using structs or classes:

```swift
@Suite("User Authentication")
struct AuthenticationTests {
    @Test func loginSucceeds() async throws {
        // ...
    }

    @Test func loginFailsWithWrongPassword() async throws {
        // ...
    }
}
```

### Nested Suites

Create hierarchical test organization:

```swift
struct UserTests {
    @Suite("Profile")
    struct ProfileTests {
        @Test func updateName() { }
        @Test func updateEmail() { }
    }

    @Suite("Settings")
    struct SettingsTests {
        @Test func changePassword() { }
        @Test func enableTwoFactor() { }
    }
}
```

### Setup and Teardown

Use initializers and deinitializers for test lifecycle:

```swift
struct DatabaseTests {
    let database: TestDatabase

    init() async throws {
        // Runs before each test
        database = try await TestDatabase.create()
        try await database.migrate()
    }

    deinit {
        // Runs after each test (for classes/actors)
    }

    @Test func queryReturnsResults() async throws {
        let results = try await database.query("SELECT * FROM users")
        #expect(!results.isEmpty)
    }
}
```

For cleanup with structs, use actors:

```swift
actor CleanupTests {
    var tempFiles: [URL] = []

    init() {
        // Setup
    }

    deinit {
        // Cleanup runs after each test
        for file in tempFiles {
            try? FileManager.default.removeItem(at: file)
        }
    }

    @Test func writeFile() async throws {
        let url = URL(fileURLWithPath: "/tmp/test.txt")
        tempFiles.append(url)
        try "test".write(to: url, atomically: true, encoding: .utf8)
    }
}
```

## Traits

Traits customize test behavior. Apply to `@Test` or `@Suite`:

### Tags

Categorize tests for filtering:

```swift
extension Tag {
    @Tag static var networking: Self
    @Tag static var database: Self
    @Tag static var slow: Self
}

@Test(.tags(.networking))
func fetchUsers() async throws { }

@Test(.tags(.database, .slow))
func migrateSchema() async throws { }

// Apply to entire suite
@Suite(.tags(.networking))
struct APITests {
    @Test func getUser() { }  // Inherits .networking tag
    @Test func createUser() { }
}
```

Run tagged tests: `swift test --filter .tags:networking`

### Conditional Execution

```swift
// Enable based on condition
@Test(.enabled(if: ProcessInfo.processInfo.environment["CI"] != nil))
func ciOnlyTest() { }

// Disable with reason
@Test(.disabled("Waiting for backend fix"))
func brokenTest() { }

// Platform-specific
@Test(.enabled(if: Platform.current == .macOS))
func macOnlyTest() { }
```

### Bug References

```swift
@Test(.bug("https://github.com/org/repo/issues/123", "Flaky on CI"))
func flakyTest() { }

@Test(.bug("JIRA-456"))
func knownIssueTest() { }
```

### Time Limits

```swift
@Test(.timeLimit(.seconds(5)))
func quickTest() async { }

@Test(.timeLimit(.minutes(1)))
func longerTest() async { }
```

### Serial Execution

By default, tests run in parallel. Use `.serialized` for tests that can't run concurrently:

```swift
@Suite(.serialized)
struct DatabaseMigrationTests {
    @Test func migration1() { }  // Runs first
    @Test func migration2() { }  // Runs after migration1
    @Test func migration3() { }  // Runs after migration2
}
```

## Parameterized Tests

Run the same test with multiple inputs:

```swift
@Test("Validates email format", arguments: [
    "user@example.com",
    "test.name@domain.org",
    "valid+tag@mail.co"
])
func validEmails(email: String) {
    #expect(EmailValidator.isValid(email))
}

@Test("Rejects invalid emails", arguments: [
    "not-an-email",
    "@missing-local.com",
    "missing-domain@"
])
func invalidEmails(email: String) {
    #expect(!EmailValidator.isValid(email))
}
```

### Multiple Parameters

```swift
@Test(arguments: [1, 2, 3], ["a", "b"])
func combinations(number: Int, letter: String) {
    // Runs for all combinations: (1,"a"), (1,"b"), (2,"a"), (2,"b"), (3,"a"), (3,"b")
    #expect(!"\(number)\(letter)".isEmpty)
}
```

### Zipped Parameters

Avoid combinatorial explosion by zipping:

```swift
@Test(arguments: zip([1, 2, 3], ["one", "two", "three"]))
func numberNames(number: Int, name: String) {
    // Runs for: (1,"one"), (2,"two"), (3,"three")
    #expect(name.count > 0)
}
```

### Collections and Ranges

```swift
@Test(arguments: 1...10)
func testRange(value: Int) {
    #expect(value > 0 && value <= 10)
}

@Test(arguments: Set(["apple", "banana", "cherry"]))
func testSet(fruit: String) {
    #expect(!fruit.isEmpty)
}
```

## Custom Test Descriptions

Implement `CustomTestStringConvertible` for better output:

```swift
struct User {
    let id: Int
    let name: String
}

extension User: CustomTestStringConvertible {
    var testDescription: String {
        "User(\(name))"
    }
}

@Test(arguments: [
    User(id: 1, name: "Alice"),
    User(id: 2, name: "Bob")
])
func userTest(user: User) {
    // Output shows "User(Alice)" instead of "User(id: 1, name: \"Alice\")"
    #expect(user.id > 0)
}
```

## Parallel Execution

Tests run in parallel by default. Each test gets an independent instance of the suite:

```swift
struct CounterTests {
    var counter = 0  // Each test gets its own counter

    @Test func increment() {
        counter += 1
        #expect(counter == 1)  // Always passes - isolated instance
    }

    @Test func decrement() {
        counter -= 1
        #expect(counter == -1)  // Always passes - isolated instance
    }
}
```

## Migration from XCTest

### Key Differences

| XCTest | Swift Testing |
|--------|---------------|
| `XCTestCase` subclass | `@Suite` struct/class |
| `func testExample()` | `@Test func example()` |
| `XCTAssertEqual(a, b)` | `#expect(a == b)` |
| `XCTAssertNil(x)` | `#expect(x == nil)` |
| `XCTAssertThrowsError` | `#expect(throws:)` |
| `XCTUnwrap(x)` | `try #require(x)` |
| `setUpWithError()` | `init() throws` |
| `tearDown()` | `deinit` |
| Sequential by default | Parallel by default |

### Coexistence

Both frameworks can run together:

```bash
swift test --enable-swift-testing
```

Don't mix assertions between frameworks in the same test. Migrate incrementally.

### Migration Example

```swift
// Before (XCTest)
class UserTests: XCTestCase {
    var sut: UserService!

    override func setUpWithError() throws {
        sut = UserService()
    }

    func testCreateUser() throws {
        let user = try sut.create(name: "Test")
        XCTAssertEqual(user.name, "Test")
        XCTAssertNotNil(user.id)
    }
}

// After (Swift Testing)
@Suite
struct UserTests {
    let sut: UserService

    init() {
        sut = UserService()
    }

    @Test func createUser() throws {
        let user = try sut.create(name: "Test")
        #expect(user.name == "Test")
        #expect(user.id != nil)
    }
}
```

## Platform Support

| Platform | Status |
|----------|--------|
| macOS | Supported |
| iOS | Supported |
| tvOS | Supported |
| watchOS | Supported |
| visionOS | Supported |
| Linux | Supported |
| Windows | Supported |

## Running Tests

```bash
# Run all tests
swift test

# Run with Swift Testing enabled (for mixed projects)
swift test --enable-swift-testing

# Filter by tag
swift test --filter .tags:networking

# Filter by name
swift test --filter UserTests
```
