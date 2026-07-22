import Darwin
import Foundation

/// Small shared helper for running system tools (`system_profiler`, `networksetup`,
/// `scutil`, `lsof`, …) from the macOS agent's capability providers.
///
/// Output is captured with a byte cap to bound memory, and the process is
/// terminated (then force-killed) if it overruns the timeout or the calling task
/// is cancelled.
enum Subprocess {
    struct Result: Sendable {
        var status: Int32
        var stdout: String
        var stderr: String
    }

    /// Maximum number of bytes captured per stream before truncation.
    static let maxOutputBytes = 256 * 1024

    static func run(
        _ executable: String,
        _ arguments: [String],
        timeout: Duration = .seconds(10)
    ) async throws -> Result {
        let process = Foundation.Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments

        let stdoutPipe = Pipe()
        let stderrPipe = Pipe()
        process.standardOutput = stdoutPipe
        process.standardError = stderrPipe

        try process.run()

        // Read both pipes to EOF concurrently on the blocking queue — never on a
        // cooperative thread — so a full pipe buffer can't deadlock the child and
        // the awaiting task only suspends. File descriptors are `Sendable`; the
        // pipes stay alive for the function's scope, keeping the fds valid.
        let stdoutFD = stdoutPipe.fileHandleForReading.fileDescriptor
        let stderrFD = stderrPipe.fileHandleForReading.fileDescriptor
        async let stdoutData = BlockingExecutor.run { Self.readCapped(fd: stdoutFD) }
        async let stderrData = BlockingExecutor.run { Self.readCapped(fd: stderrFD) }

        // Wait for exit, honoring both the timeout and task cancellation. On
        // cancellation the process is terminated so the RPC tears down instead of
        // running to completion.
        let timedOut = await withTaskCancellationHandler {
            var timedOut = false
            let deadline = ContinuousClock.now + timeout
            while process.isRunning {
                if Task.isCancelled {
                    process.terminate()
                    break
                }
                if ContinuousClock.now >= deadline {
                    timedOut = true
                    process.terminate()
                    let graceDeadline = ContinuousClock.now + .seconds(2)
                    while process.isRunning && ContinuousClock.now < graceDeadline {
                        try? await Task.sleep(for: .milliseconds(50))
                    }
                    if process.isRunning {
                        kill(process.processIdentifier, SIGKILL)
                    }
                    break
                }
                try? await Task.sleep(for: .milliseconds(25))
            }
            return timedOut
        } onCancel: {
            process.terminate()
        }

        // Poll (never block) until the child is fully reaped so `terminationStatus`
        // is valid. The pipe reads above complete once the child's write ends close.
        while process.isRunning {
            try? await Task.sleep(for: .milliseconds(10))
        }

        let out = String(decoding: await stdoutData, as: UTF8.self)
        let err = String(decoding: await stderrData, as: UTF8.self)
        return Result(
            status: timedOut ? 124 : process.terminationStatus,
            stdout: out,
            stderr: err
        )
    }

    /// Reads a file descriptor to EOF, capping the captured bytes. Runs on the
    /// blocking queue; `read` blocks until data is available or the write end
    /// closes (returning 0).
    private static func readCapped(fd: Int32) -> Data {
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 64 * 1024)
        while data.count <= maxOutputBytes {
            let bytesRead = buffer.withUnsafeMutableBytes { pointer in
                read(fd, pointer.baseAddress, pointer.count)
            }
            if bytesRead <= 0 { break }
            data.append(contentsOf: buffer[0..<bytesRead])
        }
        if data.count > maxOutputBytes {
            data = data.prefix(maxOutputBytes)
        }
        return data
    }
}
