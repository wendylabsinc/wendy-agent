import Foundation
import GRPCCore
import Testing

@testable import WendyAgentCore

@Suite("validateContainedPath")
struct PathValidationTests {
    let base = URL(fileURLWithPath: "/var/lib/wendy/apps", isDirectory: true)

    @Test("accepts a simple single component")
    func acceptsSimpleComponent() throws {
        let got = try validateContainedPath(base: base, relative: "my-app")
        #expect(got.standardizedFileURL.path == "/var/lib/wendy/apps/my-app")
    }

    @Test("accepts a multi-component relative path (blob digest shape)")
    func acceptsMultiComponent() throws {
        let got = try validateContainedPath(base: base, relative: "sha256/deadbeef")
        #expect(got.standardizedFileURL.path == "/var/lib/wendy/apps/sha256/deadbeef")
    }

    @Test("rejects dot-dot traversal")
    func rejectsDotDot() {
        #expect(throws: PathValidationError.self) {
            _ = try validateContainedPath(base: base, relative: "../../../etc/passwd")
        }
    }

    @Test("rejects an absolute path")
    func rejectsAbsolute() {
        #expect(throws: PathValidationError.self) {
            _ = try validateContainedPath(base: base, relative: "/etc/passwd")
        }
    }

    @Test("rejects empty")
    func rejectsEmpty() {
        #expect(throws: PathValidationError.self) {
            _ = try validateContainedPath(base: base, relative: "")
        }
    }

    @Test("rejects a component that escapes via embedded dot-dot")
    func rejectsEmbeddedEscape() {
        #expect(throws: PathValidationError.self) {
            _ = try validateContainedPath(base: base, relative: "a/../../b")
        }
    }

    // Regression lock for the load-bearing trailing "/" in the containment check:
    // a sibling directory that shares `base` as a string prefix (…/apps vs …/apps-evil)
    // must be rejected. Without the "+ \"/\"" a bare hasPrefix would wrongly accept it.
    @Test("rejects a prefix-sibling escape (…/apps vs …/apps-evil)")
    func rejectsPrefixSiblingEscape() {
        #expect(throws: PathValidationError.self) {
            _ = try validateContainedPath(base: base, relative: "../apps-evil/secret")
        }
    }

    @Test("PathValidationError maps to invalidArgument RPC status")
    func mapsToInvalidArgument() {
        let rpcError = RPCError(PathValidationError.unsafePath("../x"))
        #expect(rpcError.code == .invalidArgument)
        #expect(rpcError.message == "unsafe path rejected: ../x")
    }
}
