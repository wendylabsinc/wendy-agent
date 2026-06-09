# Hummingbird Testing

Hummingbird provides `HummingbirdTesting` for testing applications without starting a real server.

## Setup

Add to `Package.swift`:

```swift
.testTarget(
    name: "AppTests",
    dependencies: [
        .product(name: "HummingbirdTesting", package: "hummingbird"),
        .product(name: "Testing", package: "swift-testing")
    ]
)
```

## Basic Testing

### Test Mode

Use `.router` mode for fast in-process testing (no network):

```swift
import Hummingbird
import HummingbirdTesting
import Testing

@Test func healthEndpoint() async throws {
    let router = Router()
    router.get("/health") { _, _ -> HTTPResponse.Status in .ok }

    let app = Application(router: router)

    try await app.test(.router) { client in
        try await client.execute(uri: "/health", method: .get) { response in
            #expect(response.status == .ok)
        }
    }
}
```

### Live Server Testing

Use `.live` mode for full HTTP testing:

```swift
@Test func liveServerTest() async throws {
    let app = buildApplication()

    try await app.test(.live) { client in
        try await client.execute(uri: "/health", method: .get) { response in
            #expect(response.status == .ok)
        }
    }
}
```

## Request Testing

### GET Requests

```swift
@Test func getUser() async throws {
    let app = buildApplication()

    try await app.test(.router) { client in
        try await client.execute(uri: "/users/1", method: .get) { response in
            #expect(response.status == .ok)

            let user = try JSONDecoder().decode(
                User.self,
                from: response.body
            )
            #expect(user.id == 1)
        }
    }
}
```

### POST with JSON Body

```swift
@Test func createUser() async throws {
    let app = buildApplication()

    try await app.test(.router) { client in
        let newUser = CreateUserRequest(name: "Alice", email: "alice@example.com")
        let body = try JSONEncoder().encodeAsByteBuffer(newUser, allocator: .init())

        try await client.execute(
            uri: "/users",
            method: .post,
            headers: [.contentType: "application/json"],
            body: body
        ) { response in
            #expect(response.status == .created)

            let user = try JSONDecoder().decode(User.self, from: response.body)
            #expect(user.name == "Alice")
            #expect(user.email == "alice@example.com")
        }
    }
}
```

### With Query Parameters

```swift
@Test func searchUsers() async throws {
    let app = buildApplication()

    try await app.test(.router) { client in
        try await client.execute(
            uri: "/users?search=alice&limit=10",
            method: .get
        ) { response in
            #expect(response.status == .ok)

            let users = try JSONDecoder().decode([User].self, from: response.body)
            #expect(users.count <= 10)
        }
    }
}
```

### With Headers

```swift
@Test func authenticatedRequest() async throws {
    let app = buildApplication()

    try await app.test(.router) { client in
        try await client.execute(
            uri: "/api/me",
            method: .get,
            headers: [
                .authorization: "Bearer test-token",
                .accept: "application/json"
            ]
        ) { response in
            #expect(response.status == .ok)
        }
    }
}
```

## Response Assertions

### Status Codes

```swift
try await client.execute(uri: "/users", method: .get) { response in
    #expect(response.status == .ok)
}

try await client.execute(uri: "/users", method: .post, body: body) { response in
    #expect(response.status == .created)
}

try await client.execute(uri: "/missing", method: .get) { response in
    #expect(response.status == .notFound)
}
```

### Headers

```swift
try await client.execute(uri: "/api/data", method: .get) { response in
    #expect(response.headers[.contentType] == "application/json")
    #expect(response.headers[.cacheControl] != nil)
}
```

### JSON Body

```swift
try await client.execute(uri: "/user/1", method: .get) { response in
    let user = try JSONDecoder().decode(User.self, from: response.body)
    #expect(user.id == 1)
    #expect(user.name == "Alice")
}

// Array response
try await client.execute(uri: "/users", method: .get) { response in
    let users = try JSONDecoder().decode([User].self, from: response.body)
    #expect(users.count > 0)
}
```

### String Body

```swift
try await client.execute(uri: "/hello", method: .get) { response in
    let body = String(buffer: response.body)
    #expect(body == "Hello, World!")
}
```

## Testing with Dependencies

### Dependency Injection

```swift
// Protocol for dependency
protocol UserRepository: Sendable {
    func find(id: Int) async throws -> User?
    func create(_ user: User) async throws -> User
}

// Production implementation
struct PostgresUserRepository: UserRepository {
    // Real database calls
}

// Test implementation
actor MockUserRepository: UserRepository {
    var users: [Int: User] = [:]

    func find(id: Int) async throws -> User? {
        users[id]
    }

    func create(_ user: User) async throws -> User {
        users[user.id] = user
        return user
    }
}

// Application builder
func buildApplication(userRepository: UserRepository) -> some ApplicationProtocol {
    let router = Router()

    router.get("/users/{id}") { request, context in
        guard let id = context.parameters.get("id", as: Int.self),
              let user = try await userRepository.find(id: id) else {
            throw HTTPError(.notFound)
        }
        return user
    }

    return Application(router: router)
}

// Test
@Test func getUserWithMock() async throws {
    let mockRepo = MockUserRepository()
    await mockRepo.create(User(id: 1, name: "Alice"))

    let app = buildApplication(userRepository: mockRepo)

    try await app.test(.router) { client in
        try await client.execute(uri: "/users/1", method: .get) { response in
            #expect(response.status == .ok)
            let user = try JSONDecoder().decode(User.self, from: response.body)
            #expect(user.name == "Alice")
        }
    }
}
```

### Testing with Context

```swift
struct TestRequestContext: RequestContext, AuthRequestContext {
    var coreContext: CoreRequestContextStorage
    var auth: LoginCache

    init(source: Source) {
        self.coreContext = .init(source: source)
        self.auth = .init()
    }
}

@Test func protectedEndpoint() async throws {
    let router = Router(context: TestRequestContext.self)

    router.get("/me") { request, context -> User in
        try context.auth.require(User.self)
    }

    // Add test authenticator that always succeeds
    router.middlewares.add(TestAuthMiddleware())

    let app = Application(router: router)

    try await app.test(.router) { client in
        try await client.execute(
            uri: "/me",
            method: .get,
            headers: [.authorization: "Bearer test"]
        ) { response in
            #expect(response.status == .ok)
        }
    }
}
```

## Error Testing

```swift
@Test func notFoundError() async throws {
    let app = buildApplication()

    try await app.test(.router) { client in
        try await client.execute(uri: "/nonexistent", method: .get) { response in
            #expect(response.status == .notFound)
        }
    }
}

@Test func validationError() async throws {
    let app = buildApplication()

    try await app.test(.router) { client in
        // Send invalid JSON
        let invalidBody = ByteBuffer(string: "not json")

        try await client.execute(
            uri: "/users",
            method: .post,
            headers: [.contentType: "application/json"],
            body: invalidBody
        ) { response in
            #expect(response.status == .badRequest)
        }
    }
}

@Test func unauthorizedError() async throws {
    let app = buildApplication()

    try await app.test(.router) { client in
        // No auth header
        try await client.execute(uri: "/api/protected", method: .get) { response in
            #expect(response.status == .unauthorized)
        }
    }
}
```

## Integration Testing

### Database Integration

```swift
@Test func createUserIntegration() async throws {
    // Setup test database
    let db = try await TestDatabase.create()
    try await db.migrate()

    defer {
        Task { try await db.cleanup() }
    }

    let app = buildApplication(database: db)

    try await app.test(.router) { client in
        // Create user
        let newUser = CreateUserRequest(name: "Alice", email: "alice@test.com")
        let body = try JSONEncoder().encodeAsByteBuffer(newUser, allocator: .init())

        try await client.execute(
            uri: "/users",
            method: .post,
            headers: [.contentType: "application/json"],
            body: body
        ) { response in
            #expect(response.status == .created)
        }

        // Verify in database
        let users = try await db.query("SELECT * FROM users WHERE email = $1", ["alice@test.com"])
        #expect(users.count == 1)
    }
}
```

### Multiple Requests

```swift
@Test func userWorkflow() async throws {
    let app = buildApplication()

    try await app.test(.router) { client in
        // Create user
        let createBody = try JSONEncoder().encodeAsByteBuffer(
            CreateUserRequest(name: "Alice", email: "alice@test.com"),
            allocator: .init()
        )

        var userId: Int!

        try await client.execute(
            uri: "/users",
            method: .post,
            headers: [.contentType: "application/json"],
            body: createBody
        ) { response in
            #expect(response.status == .created)
            let user = try JSONDecoder().decode(User.self, from: response.body)
            userId = user.id
        }

        // Get user
        try await client.execute(uri: "/users/\(userId!)", method: .get) { response in
            #expect(response.status == .ok)
            let user = try JSONDecoder().decode(User.self, from: response.body)
            #expect(user.name == "Alice")
        }

        // Update user
        let updateBody = try JSONEncoder().encodeAsByteBuffer(
            UpdateUserRequest(name: "Alice Smith"),
            allocator: .init()
        )

        try await client.execute(
            uri: "/users/\(userId!)",
            method: .put,
            headers: [.contentType: "application/json"],
            body: updateBody
        ) { response in
            #expect(response.status == .ok)
        }

        // Delete user
        try await client.execute(uri: "/users/\(userId!)", method: .delete) { response in
            #expect(response.status == .noContent)
        }

        // Verify deleted
        try await client.execute(uri: "/users/\(userId!)", method: .get) { response in
            #expect(response.status == .notFound)
        }
    }
}
```

## Test Helpers

### JSON Encoding Helper

```swift
extension TestClientProtocol {
    func post<T: Encodable>(
        _ uri: String,
        body: T,
        headers: HTTPFields = [:]
    ) async throws -> TestResponse {
        var headers = headers
        headers[.contentType] = "application/json"

        let bodyBuffer = try JSONEncoder().encodeAsByteBuffer(body, allocator: .init())

        return try await execute(
            uri: uri,
            method: .post,
            headers: headers,
            body: bodyBuffer
        )
    }
}
```

### Response Decoding Helper

```swift
extension TestResponse {
    func decode<T: Decodable>(as type: T.Type) throws -> T {
        try JSONDecoder().decode(type, from: body)
    }
}

// Usage
try await client.execute(uri: "/user/1", method: .get) { response in
    let user = try response.decode(as: User.self)
    #expect(user.name == "Alice")
}
```
