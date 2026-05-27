# Hummingbird Routing

## Router Types

Hummingbird provides two router implementations:

### Trie-Based Router (Default)

Scales well for large applications:

```swift
import Hummingbird

let router = Router(context: BasicRequestContext.self)

router.get("/users") { _, _ in "List users" }
router.post("/users") { _, _ in "Create user" }
```

### Result Builder Router

Faster for small apps, uses Swift result builders:

```swift
import HummingbirdRouter

let router = RouterBuilder(context: BasicRequestContext.self) {
    Get("/") { _, _ in "Hello" }
    Get("/health") { _, _ -> HTTPResponse.Status in .ok }

    RouteGroup("api/v1") {
        RouteGroup("users") {
            Get { _, _ in "List users" }
            Post { _, _ in "Create user" }
            Get("{id}") { _, context in
                "User \(context.parameters.get("id")!)"
            }
        }
    }
}
```

## Path Parameters

### Basic Parameters

```swift
router.get("/users/{id}") { request, context in
    let id = context.parameters.get("id")!
    return "User: \(id)"
}

router.get("/posts/{postId}/comments/{commentId}") { request, context in
    let postId = context.parameters.get("postId")!
    let commentId = context.parameters.get("commentId")!
    return "Post \(postId), Comment \(commentId)"
}
```

### Typed Parameters

```swift
router.get("/users/{id}") { request, context in
    guard let id = context.parameters.get("id", as: Int.self) else {
        throw HTTPError(.badRequest, message: "Invalid user ID")
    }
    return "User ID: \(id)"
}
```

### Wildcard Paths

```swift
// Match single path component
router.get("/files/{filename}") { request, context in
    let filename = context.parameters.get("filename")!
    return "File: \(filename)"
}

// Match remaining path (catch-all)
router.get("/static/{path*}") { request, context in
    let path = context.parameters.get("path")!
    return "Serving: \(path)"
}
```

## Route Groups

### Basic Grouping

```swift
router.group("api") { api in
    api.get("/status") { _, _ in "OK" }

    api.group("v1") { v1 in
        v1.get("/users") { _, _ in "V1 users" }
    }

    api.group("v2") { v2 in
        v2.get("/users") { _, _ in "V2 users" }
    }
}
```

### Middleware on Groups

```swift
router.group("api") { api in
    // Public routes
    api.get("/health") { _, _ in "OK" }

    // Protected routes
    api.group("admin") { admin in
        admin.middlewares.add(AuthMiddleware())
        admin.get("/users") { _, _ in "Admin users" }
        admin.delete("/users/{id}") { _, _ in "Delete user" }
    }
}
```

## HTTP Methods

```swift
router.get("/resource") { _, _ in "GET" }
router.post("/resource") { _, _ in "POST" }
router.put("/resource") { _, _ in "PUT" }
router.patch("/resource") { _, _ in "PATCH" }
router.delete("/resource") { _, _ in "DELETE" }
router.head("/resource") { _, _ in "HEAD" }
router.options("/resource") { _, _ in "OPTIONS" }
```

## Query Parameters

```swift
router.get("/search") { request, context in
    let params = request.uri.queryParameters

    // Single value
    let query = params.get("q") ?? ""

    // With type conversion
    let page = params.get("page").flatMap(Int.init) ?? 1
    let limit = params.get("limit").flatMap(Int.init) ?? 20

    // Multiple values (e.g., ?tag=swift&tag=server)
    let tags = params.getAll("tag")

    return "Search: \(query), page \(page), limit \(limit), tags: \(tags)"
}
```

## Request Body

### JSON Decoding

```swift
struct CreateUserRequest: Decodable {
    let name: String
    let email: String
    let age: Int?
}

router.post("/users") { request, context in
    let input = try await request.decode(as: CreateUserRequest.self, context: context)
    // Use input.name, input.email, input.age
    return HTTPResponse.Status.created
}
```

### Raw Body

```swift
router.post("/upload") { request, context in
    let body = try await request.body.collect(upTo: 10_000_000) // 10MB limit
    // Process body bytes
    return "Received \(body.readableBytes) bytes"
}
```

### Form Data

```swift
router.post("/form") { request, context in
    let formData = try await request.decode(
        as: URLEncodedFormDecoder.self,
        context: context
    )
    // Process form fields
    return "Form submitted"
}
```

## Response Types

### Strings

```swift
router.get("/hello") { _, _ in
    "Hello, World!"
}
```

### HTTP Status

```swift
router.get("/health") { _, _ -> HTTPResponse.Status in
    .ok
}

router.delete("/users/{id}") { _, _ -> HTTPResponse.Status in
    .noContent
}
```

### Codable Types

```swift
struct User: ResponseCodable {
    let id: Int
    let name: String
}

router.get("/user") { _, _ -> User in
    User(id: 1, name: "Alice")
}

// Returns array
router.get("/users") { _, _ -> [User] in
    [User(id: 1, name: "Alice"), User(id: 2, name: "Bob")]
}
```

### Custom Response

```swift
router.get("/custom") { _, _ -> Response in
    var headers = HTTPFields()
    headers[.contentType] = "text/plain"
    headers[.cacheControl] = "max-age=3600"

    return Response(
        status: .ok,
        headers: headers,
        body: .init(byteBuffer: ByteBuffer(string: "Custom response"))
    )
}
```

### EditedResponse

```swift
router.post("/users") { request, context -> EditedResponse<User> in
    let input = try await request.decode(as: CreateUserRequest.self, context: context)
    let user = User(id: 1, name: input.name)

    return EditedResponse(
        status: .created,
        headers: [.location: "/users/\(user.id)"],
        response: user
    )
}
```

## Error Handling

### HTTPError

```swift
router.get("/users/{id}") { request, context in
    guard let id = context.parameters.get("id", as: Int.self) else {
        throw HTTPError(.badRequest, message: "Invalid ID format")
    }

    guard let user = try await userService.find(id: id) else {
        throw HTTPError(.notFound, message: "User not found")
    }

    return user
}
```

### Custom Error Responses

```swift
struct APIError: Error, ResponseGenerator {
    let status: HTTPResponse.Status
    let code: String
    let message: String

    func response(from request: Request, context: some RequestContext) throws -> Response {
        struct ErrorBody: Encodable {
            let code: String
            let message: String
        }

        let body = ErrorBody(code: code, message: message)
        var response = try JSONEncoder().encodeAsByteBuffer(body, allocator: .init())

        return Response(
            status: status,
            headers: [.contentType: "application/json"],
            body: .init(byteBuffer: response)
        )
    }
}

router.get("/resource") { _, _ in
    throw APIError(status: .forbidden, code: "ACCESS_DENIED", message: "Insufficient permissions")
}
```

## Redirects

```swift
router.get("/old-path") { _, _ -> Response in
    Response.redirect(to: "/new-path", type: .permanent)
}

router.get("/temporary") { _, _ -> Response in
    Response.redirect(to: "/other", type: .temporary)
}
```

## File Downloads

```swift
router.get("/download/{filename}") { request, context -> Response in
    let filename = context.parameters.get("filename")!
    let filePath = "files/\(filename)"

    guard FileManager.default.fileExists(atPath: filePath) else {
        throw HTTPError(.notFound)
    }

    var headers = HTTPFields()
    headers[.contentDisposition] = "attachment; filename=\"\(filename)\""

    return Response(
        status: .ok,
        headers: headers,
        body: .init(asyncSequence: FileIO.readFile(at: filePath))
    )
}
```
