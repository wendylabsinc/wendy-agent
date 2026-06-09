# Hummingbird Request Context

Request contexts are statically-typed metadata containers associated with each request. They're the primary mechanism for passing data through middleware chains and into route handlers.

## RequestContext Protocol

Every context must conform to `RequestContext` and store a `CoreRequestContextStorage`:

```swift
import Hummingbird

struct AppRequestContext: RequestContext {
    var coreContext: CoreRequestContextStorage

    init(source: Source) {
        self.coreContext = .init(source: source)
    }
}

let router = Router(context: AppRequestContext.self)
```

### CoreRequestContextStorage

The `CoreRequestContextStorage` holds framework-managed properties:
- Request decoder/encoder
- Logger
- Path parameters
- Maximum upload size

Always initialize it from the source in your `init`.

## Custom Context Properties

Add properties to pass data through middleware to handlers:

```swift
struct AppRequestContext: RequestContext {
    var coreContext: CoreRequestContextStorage

    // Custom properties
    var requestId: String?
    var authenticatedUser: User?
    var permissions: Set<Permission> = []

    init(source: Source) {
        self.coreContext = .init(source: source)
    }
}

// Middleware sets properties
struct RequestIdMiddleware: RouterMiddleware {
    func handle(
        _ request: Request,
        context: AppRequestContext,
        next: (Request, AppRequestContext) async throws -> Response
    ) async throws -> Response {
        var context = context
        context.requestId = UUID().uuidString
        return try await next(request, context)
    }
}

// Handler reads properties
router.get("/debug") { request, context in
    "Request ID: \(context.requestId ?? "none")"
}
```

## Context Protocols (Composition)

Define protocols for context capabilities to make middleware reusable across different context types:

```swift
// Define capability protocols
protocol RequestIdContext: RequestContext {
    var requestId: String? { get set }
}

protocol AuthenticatedContext: RequestContext {
    var authenticatedUser: User? { get set }
}

protocol PermissionContext: RequestContext {
    var permissions: Set<Permission> { get set }
}

// Middleware uses protocol constraints
struct RequestIdMiddleware<Context: RequestIdContext>: RouterMiddleware {
    func handle(
        _ request: Request,
        context: Context,
        next: (Request, Context) async throws -> Response
    ) async throws -> Response {
        var context = context
        context.requestId = UUID().uuidString
        return try await next(request, context)
    }
}

// Context conforms to multiple protocols
struct AppRequestContext: RequestContext, RequestIdContext, AuthenticatedContext, PermissionContext {
    var coreContext: CoreRequestContextStorage
    var requestId: String?
    var authenticatedUser: User?
    var permissions: Set<Permission> = []

    init(source: Source) {
        self.coreContext = .init(source: source)
    }
}
```

## ChildRequestContext

`ChildRequestContext` creates a new context from an existing parent context. This is useful for:
- Transforming context after authentication
- Requiring certain properties to be set
- Narrowing context type for specific route groups

### Protocol Definition

```swift
protocol ChildRequestContext<ParentContext>: RequestContext where Self.Source == Never {
    associatedtype ParentContext: RequestContext
    init(context: ParentContext) throws
}
```

### Authentication Example

Transform an auth context into a context that guarantees a user exists:

```swift
import HummingbirdAuth

// Parent context with optional user
struct AppRequestContext: AuthRequestContext {
    var coreContext: CoreRequestContextStorage
    var auth: LoginCache

    init(source: Source) {
        self.coreContext = .init(source: source)
        self.auth = .init()
    }
}

// Child context that requires authenticated user
struct AuthenticatedRequestContext: ChildRequestContext {
    typealias ParentContext = AppRequestContext

    var coreContext: CoreRequestContextStorage
    var user: User

    init(context: AppRequestContext) throws {
        self.coreContext = context.coreContext
        // Throws if user not authenticated
        self.user = try context.auth.require(User.self)
    }
}
```

### Using Child Context in Routes

```swift
let router = Router(context: AppRequestContext.self)

// Public routes use AppRequestContext
router.get("/public") { request, context in
    "Anyone can access"
}

// Protected routes transform to AuthenticatedRequestContext
router.group("api")
    .add(middleware: BearerAuthenticator())
    .group(context: AuthenticatedRequestContext.self) { protected in
        // All routes here have guaranteed user
        protected.get("/me") { request, context -> User in
            // context.user is non-optional, guaranteed to exist
            context.user
        }

        protected.get("/profile") { request, context in
            "Hello, \(context.user.name)"
        }
    }
```

### Multi-Level Child Contexts

Chain child contexts for progressive refinement:

```swift
// Level 1: Basic auth context
struct AppRequestContext: AuthRequestContext {
    var coreContext: CoreRequestContextStorage
    var auth: LoginCache

    init(source: Source) {
        self.coreContext = .init(source: source)
        self.auth = .init()
    }
}

// Level 2: Authenticated user context
struct UserContext: ChildRequestContext {
    typealias ParentContext = AppRequestContext

    var coreContext: CoreRequestContextStorage
    var user: User

    init(context: AppRequestContext) throws {
        self.coreContext = context.coreContext
        self.user = try context.auth.require(User.self)
    }
}

// Level 3: Admin user context
struct AdminContext: ChildRequestContext {
    typealias ParentContext = UserContext

    var coreContext: CoreRequestContextStorage
    var admin: User

    init(context: UserContext) throws {
        self.coreContext = context.coreContext
        guard context.user.isAdmin else {
            throw HTTPError(.forbidden, message: "Admin access required")
        }
        self.admin = context.user
    }
}

// Route setup
let router = Router(context: AppRequestContext.self)

router.group("api")
    .add(middleware: BearerAuthenticator())
    .group(context: UserContext.self) { user in
        // Authenticated user routes
        user.get("/me") { _, context in context.user }

        // Admin-only routes
        user.group("admin", context: AdminContext.self) { admin in
            admin.get("/users") { _, context in
                // context.admin guaranteed to be admin user
                try await userService.listAll()
            }

            admin.delete("/users/{id}") { _, context in
                // Admin-only deletion
            }
        }
    }
```

### Child Context with Additional Data

Pass extra data during context transformation:

```swift
struct TenantContext: ChildRequestContext {
    typealias ParentContext = AppRequestContext

    var coreContext: CoreRequestContextStorage
    var user: User
    var tenant: Tenant

    init(context: AppRequestContext) throws {
        self.coreContext = context.coreContext
        self.user = try context.auth.require(User.self)

        // Load tenant from user
        guard let tenant = user.tenant else {
            throw HTTPError(.forbidden, message: "No tenant associated")
        }
        self.tenant = tenant
    }
}

router.group("tenant", context: TenantContext.self) { tenant in
    tenant.get("/info") { _, context in
        // Access both user and tenant
        TenantInfo(
            tenantName: context.tenant.name,
            userName: context.user.name
        )
    }
}
```

## AuthRequestContext

Hummingbird Auth provides `AuthRequestContext` protocol for authentication:

```swift
import HummingbirdAuth

struct AppRequestContext: AuthRequestContext {
    var coreContext: CoreRequestContextStorage
    var auth: LoginCache  // Required by AuthRequestContext

    init(source: Source) {
        self.coreContext = .init(source: source)
        self.auth = .init()
    }
}
```

### LoginCache Methods

```swift
// Store authenticated identity
context.auth.login(user)

// Get identity (returns nil if not authenticated)
let user = context.auth.get(User.self)

// Require identity (throws 401 if not authenticated)
let user = try context.auth.require(User.self)

// Check if authenticated
if context.auth.isAuthenticated(User.self) {
    // ...
}

// Logout
context.auth.logout(User.self)
```

## Request Decoder/Encoder Customization

Override default JSON encoding per context:

```swift
struct XMLRequestContext: RequestContext {
    var coreContext: CoreRequestContextStorage

    // Custom decoder
    var requestDecoder: RequestDecoder {
        XMLDecoder()
    }

    // Custom encoder
    var responseEncoder: ResponseEncoder {
        XMLEncoder()
    }

    init(source: Source) {
        self.coreContext = .init(source: source)
    }
}
```

## Best Practices

### 1. Use Protocol Composition

Define small, focused protocols for context capabilities:

```swift
protocol HasLogger: RequestContext {
    var logger: Logger { get }
}

protocol HasDatabase: RequestContext {
    var database: Database { get }
}

protocol HasUser: RequestContext {
    var user: User { get }
}
```

### 2. Leverage Child Contexts for Type Safety

Use child contexts to guarantee properties exist rather than optionals:

```swift
// Instead of checking optionals everywhere:
router.get("/profile") { request, context in
    guard let user = context.user else {
        throw HTTPError(.unauthorized)
    }
    return user.profile
}

// Use child context for guaranteed access:
router.group(context: AuthenticatedContext.self) { auth in
    auth.get("/profile") { request, context in
        context.user.profile  // Non-optional, guaranteed
    }
}
```

### 3. Keep Contexts Lightweight

Only store references or small values. Avoid heavy objects:

```swift
// Good: Store identifier, fetch when needed
struct AppRequestContext: RequestContext {
    var coreContext: CoreRequestContextStorage
    var userId: UUID?  // Lightweight
}

// Avoid: Storing large objects
struct AppRequestContext: RequestContext {
    var coreContext: CoreRequestContextStorage
    var fullUserProfile: UserProfile?  // Heavy object
}
```

### 4. Document Context Flow

Comment which middleware sets which properties:

```swift
struct AppRequestContext: RequestContext {
    var coreContext: CoreRequestContextStorage

    /// Set by RequestIdMiddleware
    var requestId: String?

    /// Set by AuthMiddleware after successful authentication
    var user: User?

    /// Set by TenantMiddleware, requires user to be set first
    var tenant: Tenant?
}
```
