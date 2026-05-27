# Hummingbird Middleware

Middleware intercepts requests before handlers and responses after, enabling cross-cutting concerns like logging, authentication, and CORS.

## Creating Middleware

### RouterMiddleware Protocol

```swift
import Hummingbird

struct LoggingMiddleware<Context: RequestContext>: RouterMiddleware {
    func handle(
        _ request: Request,
        context: Context,
        next: (Request, Context) async throws -> Response
    ) async throws -> Response {
        let start = ContinuousClock.now

        // Before handler
        print("→ \(request.method) \(request.uri.path)")

        // Call next middleware/handler
        let response = try await next(request, context)

        // After handler
        let duration = ContinuousClock.now - start
        print("← \(response.status) (\(duration))")

        return response
    }
}
```

### Applying Middleware

```swift
let router = Router()

// Apply to all routes
router.middlewares.add(LoggingMiddleware())

// Apply to route group
router.group("api") { api in
    api.middlewares.add(AuthMiddleware())
    api.get("/protected") { _, _ in "Secret" }
}
```

## Common Middleware Patterns

### Request Modification

```swift
struct RequestIdMiddleware<Context: RequestContext>: RouterMiddleware {
    func handle(
        _ request: Request,
        context: Context,
        next: (Request, Context) async throws -> Response
    ) async throws -> Response {
        var request = request
        let requestId = UUID().uuidString
        request.headers[.init("X-Request-ID")!] = requestId

        return try await next(request, context)
    }
}
```

### Response Modification

```swift
struct SecurityHeadersMiddleware<Context: RequestContext>: RouterMiddleware {
    func handle(
        _ request: Request,
        context: Context,
        next: (Request, Context) async throws -> Response
    ) async throws -> Response {
        var response = try await next(request, context)

        response.headers[.init("X-Content-Type-Options")!] = "nosniff"
        response.headers[.init("X-Frame-Options")!] = "DENY"
        response.headers[.init("X-XSS-Protection")!] = "1; mode=block"

        return response
    }
}
```

### Early Return (Short-Circuit)

```swift
struct MaintenanceMiddleware<Context: RequestContext>: RouterMiddleware {
    let isMaintenanceMode: Bool

    func handle(
        _ request: Request,
        context: Context,
        next: (Request, Context) async throws -> Response
    ) async throws -> Response {
        if isMaintenanceMode {
            return Response(
                status: .serviceUnavailable,
                headers: [.contentType: "application/json"],
                body: .init(byteBuffer: ByteBuffer(string: #"{"error":"Maintenance mode"}"#))
            )
        }

        return try await next(request, context)
    }
}
```

### Error Handling

```swift
struct ErrorHandlingMiddleware<Context: RequestContext>: RouterMiddleware {
    func handle(
        _ request: Request,
        context: Context,
        next: (Request, Context) async throws -> Response
    ) async throws -> Response {
        do {
            return try await next(request, context)
        } catch let error as HTTPError {
            throw error // Re-throw HTTP errors
        } catch {
            // Convert unknown errors to 500
            print("Unexpected error: \(error)")
            throw HTTPError(.internalServerError, message: "Internal server error")
        }
    }
}
```

## Built-in Middleware

### CORS Middleware

```swift
import Hummingbird

router.middlewares.add(CORSMiddleware(
    allowOrigin: .originBased,  // or .all, or .custom("https://example.com")
    allowHeaders: [.contentType, .authorization, .accept],
    allowMethods: [.get, .post, .put, .delete, .patch],
    allowCredentials: true,
    exposedHeaders: [.init("X-Request-ID")!],
    maxAge: .seconds(3600)
))
```

### File Middleware

Serve static files:

```swift
router.middlewares.add(FileMiddleware(
    rootFolder: "public",
    searchForIndexHtml: true
))
```

### Metrics Middleware

With swift-metrics:

```swift
import Hummingbird

router.middlewares.add(MetricsMiddleware())
```

### Tracing Middleware

With swift-distributed-tracing:

```swift
import Hummingbird

router.middlewares.add(TracingMiddleware())
```

### Logging Middleware

```swift
router.middlewares.add(LogRequestsMiddleware(.info))
```

## Authentication Middleware

### Basic Authentication

```swift
import HummingbirdAuth

struct BasicAuthenticator: AuthenticatorMiddleware {
    func authenticate(
        request: Request,
        context: some AuthRequestContext
    ) async throws -> User? {
        guard let basic = request.headers.basic else {
            return nil
        }

        // Validate credentials
        guard let user = try await userService.authenticate(
            username: basic.username,
            password: basic.password
        ) else {
            return nil
        }

        return user
    }
}

// Apply to routes
router.group("api") { api in
    api.add(middleware: BasicAuthenticator())
    api.get("/me") { request, context -> User in
        try context.auth.require(User.self)
    }
}
```

### Bearer Token Authentication

```swift
import HummingbirdAuth

struct BearerAuthenticator: AuthenticatorMiddleware {
    func authenticate(
        request: Request,
        context: some AuthRequestContext
    ) async throws -> User? {
        guard let bearer = request.headers.bearer else {
            return nil
        }

        // Validate token and get user
        guard let user = try await tokenService.validate(token: bearer.token) else {
            return nil
        }

        return user
    }
}
```

### JWT Authentication

```swift
import HummingbirdAuth
import JWTKit

struct JWTAuthenticator: AuthenticatorMiddleware {
    let keys: JWTKeyCollection

    func authenticate(
        request: Request,
        context: some AuthRequestContext
    ) async throws -> User? {
        guard let bearer = request.headers.bearer else {
            return nil
        }

        do {
            let payload = try await keys.verify(bearer.token, as: UserPayload.self)
            return User(id: payload.userId, email: payload.email)
        } catch {
            return nil
        }
    }
}
```

### Requiring Authentication

```swift
router.group("api") { api in
    // Add authenticator
    api.add(middleware: BearerAuthenticator())

    // Public endpoint (authentication optional)
    api.get("/public") { _, _ in "Anyone can see this" }

    // Protected endpoint (authentication required)
    api.get("/me") { request, context -> User in
        // Throws 401 if not authenticated
        try context.auth.require(User.self)
    }

    // Check authentication without requiring
    api.get("/profile") { request, context -> String in
        if let user = context.auth.get(User.self) {
            return "Hello, \(user.name)"
        } else {
            return "Hello, guest"
        }
    }
}
```

## Rate Limiting

```swift
import Foundation

actor RateLimiter {
    private var requests: [String: [Date]] = [:]
    private let limit: Int
    private let window: TimeInterval

    init(limit: Int, window: TimeInterval) {
        self.limit = limit
        self.window = window
    }

    func isAllowed(identifier: String) -> Bool {
        let now = Date()
        let windowStart = now.addingTimeInterval(-window)

        // Clean old entries
        requests[identifier] = (requests[identifier] ?? []).filter { $0 > windowStart }

        // Check limit
        guard (requests[identifier]?.count ?? 0) < limit else {
            return false
        }

        // Record request
        requests[identifier, default: []].append(now)
        return true
    }
}

struct RateLimitMiddleware<Context: RequestContext>: RouterMiddleware {
    let limiter: RateLimiter

    func handle(
        _ request: Request,
        context: Context,
        next: (Request, Context) async throws -> Response
    ) async throws -> Response {
        let identifier = request.headers[.init("X-Forwarded-For")!] ?? "unknown"

        guard await limiter.isAllowed(identifier: identifier) else {
            throw HTTPError(.tooManyRequests, message: "Rate limit exceeded")
        }

        return try await next(request, context)
    }
}

// Usage
let limiter = RateLimiter(limit: 100, window: 60) // 100 requests per minute
router.middlewares.add(RateLimitMiddleware(limiter: limiter))
```

## Context-Modifying Middleware

```swift
// Custom context with additional properties
struct AppRequestContext: RequestContext {
    var coreContext: CoreRequestContextStorage
    var requestId: String?

    init(source: Source) {
        self.coreContext = .init(source: source)
    }
}

// Middleware that modifies context
struct RequestIdMiddleware: RouterMiddleware {
    func handle(
        _ request: Request,
        context: AppRequestContext,
        next: (Request, AppRequestContext) async throws -> Response
    ) async throws -> Response {
        var context = context
        context.requestId = UUID().uuidString

        var response = try await next(request, context)
        response.headers[.init("X-Request-ID")!] = context.requestId

        return response
    }
}

// Access in handler
router.get("/test") { request, context in
    "Request ID: \(context.requestId ?? "none")"
}
```

## Middleware Order

Middleware executes in the order added. Request flows down, response flows up:

```swift
router.middlewares.add(LoggingMiddleware())      // 1st request, 4th response
router.middlewares.add(AuthMiddleware())          // 2nd request, 3rd response
router.middlewares.add(ValidationMiddleware())    // 3rd request, 2nd response
// Handler                                        // 4th request, 1st response
```

Typical order:
1. Logging/Metrics (outer - sees all requests/responses)
2. Error handling
3. Security headers
4. CORS
5. Rate limiting
6. Authentication
7. Authorization
8. Validation
