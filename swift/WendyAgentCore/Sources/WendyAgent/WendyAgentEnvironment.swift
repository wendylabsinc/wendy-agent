import Foundation

public enum WendyAgentEnvironment {
    public static var host: String? {
        guard let host = value("WENDY_AGENT_HOST") else {
            return nil
        }

        switch host {
        case "127.0.0.1", "::1", "localhost":
            return host
        default:
            return nil
        }
    }

    public static var port: Int? {
        port("WENDY_AGENT_PORT", minimum: 1)
    }

    public static var otelPort: Int? {
        port("WENDY_OTEL_PORT", minimum: 0)
    }

    public static var appPath: String? {
        path("WENDY_AGENT_APP_PATH")
    }

    public static var sandboxProfile: String? {
        path("WENDY_AGENT_SANDBOX_PROFILE")
    }

    private static func value(_ name: String) -> String? {
        guard let value = ProcessInfo.processInfo.environment[name],
            !value.isEmpty,
            !value.unicodeScalars.contains(where: { $0.value < 32 || $0.value == 127 })
        else {
            return nil
        }
        return value
    }

    private static func path(_ name: String) -> String? {
        guard let value = value(name) else {
            return nil
        }
        let components = value.split(separator: "/", omittingEmptySubsequences: false)
        guard !components.contains("..") else {
            return nil
        }
        return value
    }

    private static func port(_ name: String, minimum: Int) -> Int? {
        guard let value = value(name),
            let port = Int(value),
            (minimum...65_535).contains(port)
        else {
            return nil
        }
        return port
    }
}
