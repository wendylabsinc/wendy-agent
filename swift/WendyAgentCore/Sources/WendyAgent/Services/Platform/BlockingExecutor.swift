import Dispatch

/// Runs blocking, synchronous work off the Swift concurrency cooperative thread
/// pool.
///
/// Many macOS system APIs the agent relies on are synchronous and can block for
/// a meaningful time — CoreWLAN's `scanForNetworks` blocks for seconds while the
/// radio scans, CoreAudio HAL queries can stall while devices come and go. The
/// gRPC service handlers are `nonisolated`, so (with `NonisolatedNonsendingByDefault`)
/// they execute on the shared cooperative pool. Calling a blocking API directly
/// from a handler ties up one of the pool's very few threads for the whole call
/// and can starve every other in-flight request.
///
/// `Task.detached` does **not** help here: detached tasks still run on the
/// cooperative pool. This helper hops the work onto a dedicated concurrent
/// `DispatchQueue` and bridges the result back with a continuation, so the
/// awaiting task merely suspends instead of blocking a cooperative thread.
enum BlockingExecutor {
    private static let queue = DispatchQueue(
        label: "sh.wendy.agent.blocking",
        qos: .userInitiated,
        attributes: .concurrent
    )

    /// Runs `body` on the blocking queue and returns its result.
    static func run<T: Sendable>(_ body: @escaping @Sendable () -> T) async -> T {
        await withCheckedContinuation { continuation in
            queue.async {
                continuation.resume(returning: body())
            }
        }
    }

    /// Runs a throwing `body` on the blocking queue, propagating its error.
    static func run<T: Sendable>(_ body: @escaping @Sendable () throws -> T) async throws -> T {
        try await withCheckedThrowingContinuation { continuation in
            queue.async {
                continuation.resume(with: Result { try body() })
            }
        }
    }
}
