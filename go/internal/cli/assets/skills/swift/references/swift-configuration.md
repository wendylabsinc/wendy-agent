# Swift Configuration

Swift Configuration is Apple's official package for reading application and library configuration. It provides an abstraction layer that separates how code reads configuration (through a consistent API) from where that configuration comes from (various providers).

## Core Concepts

### ConfigReader

The main interface applications use to access configuration values:

```swift
import Configuration

let config = ConfigReader(providers: [
    EnvironmentVariablesProvider(),
    try await FileProvider<JSONSnapshot>(filePath: "/etc/config.json")
])

// Read values with type-safe accessors
let timeout = config.int(forKey: "http.timeout", default: 60)
let host = config.string(forKey: "database.host", default: "localhost")
let debug = config.bool(forKey: "app.debug", default: false)
```

### ConfigProvider

The backend abstraction that retrieves values from specific sources. Providers are stacked hierarchically where higher-priority sources (listed first) override lower ones.

## Built-In Providers

| Provider | Description |
|----------|-------------|
| `EnvironmentVariablesProvider` | Reads from environment variables |
| `FileProvider<JSONSnapshot>` | Reads from JSON files |
| `FileProvider<YAMLSnapshot>` | Reads from YAML files (requires YAML trait) |
| `CommandLineArgumentsProvider` | Reads from CLI arguments (requires trait) |
| `DirectoryFilesProvider` | Reads from a directory of files |
| `InMemoryProvider` | Static in-memory configuration |
| `MutableInMemoryProvider` | Mutable in-memory configuration |
| `KeyMappingProvider` | Transforms keys from another provider |

## Basic Usage

### Setting Up Configuration

```swift
import Configuration

@main
struct MyApp {
    static func main() async throws {
        // Create configuration with provider hierarchy
        // Earlier providers take precedence over later ones
        let config = ConfigReader(providers: [
            // 1. Environment variables (highest priority - for overrides)
            EnvironmentVariablesProvider(),

            // 2. Local config file (development overrides)
            try? await FileProvider<JSONSnapshot>(filePath: "./config.local.json"),

            // 3. Default config file (base configuration)
            try await FileProvider<JSONSnapshot>(filePath: "./config.json")
        ].compactMap { $0 })

        let server = MyServer(config: config)
        try await server.run()
    }
}
```

### Reading Values

```swift
// With defaults (recommended)
let port = config.int(forKey: "server.port", default: 8080)
let host = config.string(forKey: "server.host", default: "0.0.0.0")
let maxConnections = config.int(forKey: "server.maxConnections", default: 100)

// Optional values (returns nil if not found)
let apiKey: String? = config.string(forKey: "api.key")

// Nested keys use dot notation
let dbHost = config.string(forKey: "database.primary.host", default: "localhost")
let dbPort = config.int(forKey: "database.primary.port", default: 5432)
```

### Namespacing

Scope a reader to a configuration subtree:

```swift
let config = ConfigReader(providers: [...])

// Create a namespaced reader for database configuration
let dbConfig = config.namespaced("database")
let host = dbConfig.string(forKey: "host", default: "localhost")  // reads "database.host"
let port = dbConfig.int(forKey: "port", default: 5432)            // reads "database.port"

// Pass namespaced config to components
let database = Database(config: dbConfig)
```

## Provider Hierarchy

Providers are evaluated in order. The first provider to return a value wins:

```swift
let config = ConfigReader(providers: [
    // Priority 1: Environment variables (for deployment overrides)
    EnvironmentVariablesProvider(),

    // Priority 2: Command-line arguments (for runtime overrides)
    CommandLineArgumentsProvider(),

    // Priority 3: Local file (for development)
    try? await FileProvider<JSONSnapshot>(filePath: "./config.local.json"),

    // Priority 4: Default file (base configuration)
    try await FileProvider<JSONSnapshot>(filePath: "./config.json"),

    // Priority 5: In-memory defaults (fallback)
    InMemoryProvider(values: [
        "server.port": "8080",
        "server.host": "0.0.0.0"
    ])
].compactMap { $0 })
```

This allows:
- Environment variables to override everything (production deployments)
- CLI arguments for one-off changes
- Local config files for development without modifying tracked files
- Default files for base configuration
- In-memory fallbacks for sensible defaults

## Hot Reloading

Enable auto-reloading when configuration files change:

```swift
import Configuration

// Enable reloading trait in Package.swift first
let config = ConfigReader(providers: [
    EnvironmentVariablesProvider(),
    try await ReloadingFileProvider<JSONSnapshot>(filePath: "/etc/myapp/config.json")
])

// Watch for configuration changes
for await _ in config.changes {
    print("Configuration updated")
    // Reconfigure services as needed
}
```

## Package Traits

Enable optional functionality via traits in `Package.swift`:

```swift
dependencies: [
    .package(
        url: "https://github.com/apple/swift-configuration",
        from: "1.0.0",
        traits: [.defaults, "YAML", "Reloading", "CommandLineArguments"]
    )
]
```

| Trait | Description |
|-------|-------------|
| `JSON` | JSON file support (default) |
| `YAML` | YAML file support |
| `Logging` | Integration with swift-log |
| `Reloading` | Hot-reload configuration files |
| `CommandLineArguments` | CLI argument provider |

## Secret Handling

Redact sensitive values in logs and debug output:

```swift
// Mark keys as secrets for redaction
let config = ConfigReader(
    providers: [...],
    secretKeys: ["api.key", "database.password", "jwt.secret"]
)

// When logging, secrets are automatically redacted
logger.info("Config: \(config.debugDescription)")
// Output: Config: {api.key: <redacted>, server.port: 8080, ...}
```

## Best Practices

### 1. Centralize Configuration Setup

Configure providers once at application startup, not in libraries:

```swift
// Good: Application configures providers
@main
struct MyApp {
    static func main() async throws {
        let config = ConfigReader(providers: [...])
        let service = MyService(config: config)
    }
}

// Bad: Library creates its own providers
struct MyLibrary {
    init() {
        // Don't do this - let the application configure providers
        let config = ConfigReader(providers: [
            EnvironmentVariablesProvider()
        ])
    }
}
```

### 2. Accept Configuration in Libraries

Libraries should accept a `ConfigReader` or namespaced reader:

```swift
public struct DatabaseClient {
    private let host: String
    private let port: Int

    public init(config: ConfigReader) {
        self.host = config.string(forKey: "host", default: "localhost")
        self.port = config.int(forKey: "port", default: 5432)
    }
}

// Application usage
let dbConfig = config.namespaced("database")
let client = DatabaseClient(config: dbConfig)
```

### 3. Provide Sensible Defaults

Always include fallback defaults for optional configuration:

```swift
// Good: Provides defaults
let timeout = config.int(forKey: "http.timeout", default: 30)
let retries = config.int(forKey: "http.retries", default: 3)

// Avoid: No defaults for optional config
let timeout = config.int(forKey: "http.timeout")!  // Will crash if missing
```

### 4. Use Environment Variables for Deployment

Place environment variables first for easy deployment overrides:

```swift
let config = ConfigReader(providers: [
    EnvironmentVariablesProvider(),  // Allows: HTTP_TIMEOUT=60 ./myapp
    try await FileProvider<JSONSnapshot>(filePath: "./config.json")
])
```

### 5. Separate Configuration by Environment

```swift
let environment = ProcessInfo.processInfo.environment["APP_ENV"] ?? "development"

let config = ConfigReader(providers: [
    EnvironmentVariablesProvider(),
    try await FileProvider<JSONSnapshot>(filePath: "./config.\(environment).json"),
    try await FileProvider<JSONSnapshot>(filePath: "./config.json")
])
```

## Configuration File Examples

### JSON Configuration

```json
{
    "server": {
        "host": "0.0.0.0",
        "port": 8080,
        "maxConnections": 1000
    },
    "database": {
        "host": "localhost",
        "port": 5432,
        "name": "myapp",
        "pool": {
            "minConnections": 5,
            "maxConnections": 20
        }
    },
    "logging": {
        "level": "info"
    }
}
```

### YAML Configuration

```yaml
server:
  host: 0.0.0.0
  port: 8080
  maxConnections: 1000

database:
  host: localhost
  port: 5432
  name: myapp
  pool:
    minConnections: 5
    maxConnections: 20

logging:
  level: info
```

### Environment Variables

Environment variables use uppercase with underscores:

```bash
# Overrides server.port
export SERVER_PORT=9090

# Overrides database.host
export DATABASE_HOST=db.example.com

# Overrides database.pool.maxConnections
export DATABASE_POOL_MAX_CONNECTIONS=50
```

## Platform Support

| Platform | Minimum Version |
|----------|-----------------|
| macOS | 15+ |
| iOS | 18+ |
| tvOS | 18+ |
| watchOS | 11+ |
| visionOS | 2+ |
| Linux | Supported |
