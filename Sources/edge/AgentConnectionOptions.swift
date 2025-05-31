import ArgumentParser
import Foundation

struct AgentConnectionOptions: ParsableArguments {
    struct Endpoint: ExpressibleByArgument {
        let host: String
        let port: Int

        init?(argument: String) {
            // Create a dummy URL to use URLComponents parsing capabilities
            var urlString = argument
            let hasScheme = urlString.contains("://")

            // Only allow edge:// scheme or no scheme
            if hasScheme {
                if !urlString.starts(with: "edge://") {
                    return nil
                }
            } else {
                urlString = "edge://" + urlString
            }

            guard let components = URLComponents(string: urlString),
                let host = components.host, !host.isEmpty
            else {
                return nil
            }

            // Handle IPv6 addresses by removing the brackets if present
            var cleanHost = host
            if cleanHost.first == "[" && cleanHost.last == "]" {
                cleanHost = String(cleanHost.dropFirst().dropLast())
            }

            self.host = cleanHost
            self.port = components.port ?? 50051
        }

        static var defaultValueDescription: String {
            "localhost:50051"
        }

        var description: String {
            "\(host):\(port)"
        }
    }

    @Option(
        name: .shortAndLong,
        help:
            "The host and port of the Edge Agent to connect to (format: host or host:port). IPv6 addresses must be enclosed in square brackets, e.g. [2001:db8::1] or [2001:db8::1]:8080. Defaults to the `EDGE_AGENT` environment variable."
    )
    var device: Endpoint?

    @Option(
        name: .shortAndLong,
        help:
            """
            Alias for the `--device` option. (Deprecated)
            If both `--device` and `--agent` are provided, the `--device` option takes precedence.
            """
    )
    var agent: Endpoint?

    var endpoint: Endpoint {
        get throws {
            if let device {
                return device
            }

            if let agent {
                return agent
            }

            guard
                let endpoint = ProcessInfo.processInfo.environment["EDGE_AGENT"],
                let endpoint = Endpoint(argument: endpoint)
            else {
                throw ValidationError(
                    "The `--agent` option was not provided and the `EDGE_AGENT` environment variable is not set."
                )
            }

            return endpoint
        }
    }
}
