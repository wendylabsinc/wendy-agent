import Foundation
import Testing

@testable import WendyAgentCore

@Suite("Subprocess")
struct SubprocessTests {
    @Test("captures stdout of a successful command")
    func echoesStdout() async throws {
        let result = try await Subprocess.run("/bin/echo", ["hello"])
        #expect(result.status == 0)
        #expect(result.stdout.trimmingCharacters(in: .whitespacesAndNewlines) == "hello")
        #expect(result.stderr.isEmpty)
    }

    @Test("reports non-zero exit status")
    func reportsFailureStatus() async throws {
        let result = try await Subprocess.run("/bin/sh", ["-c", "exit 3"])
        #expect(result.status == 3)
    }

    @Test("captures stderr")
    func capturesStderr() async throws {
        let result = try await Subprocess.run("/bin/sh", ["-c", "echo oops 1>&2"])
        #expect(result.stderr.trimmingCharacters(in: .whitespacesAndNewlines) == "oops")
    }
}
