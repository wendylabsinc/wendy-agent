/// Which Linux container runtime backend is available on this Mac.
enum LinuxRuntimeKind: Equatable, Sendable {
    case appleContainer
    case docker
}

enum LinuxRuntimeSelector {
    /// Prefer Apple's `container`; fall back to Docker; nil if neither present.
    static func choose(containerAvailable: Bool, dockerAvailable: Bool) -> LinuxRuntimeKind? {
        if containerAvailable { return .appleContainer }
        if dockerAvailable { return .docker }
        return nil
    }
}
