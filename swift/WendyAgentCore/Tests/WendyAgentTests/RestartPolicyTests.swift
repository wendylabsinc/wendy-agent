import Foundation
import Testing
import WendyAgentGRPC

@testable import WendyAgentCore

@Suite("PersistedRestartPolicy")
struct RestartPolicyTests {
    @Test("proto DEFAULT maps to unless-stopped")
    func protoDefaultMapsToUnlessStopped() {
        var proto = RestartPolicy()
        proto.mode = .default

        let persisted = PersistedRestartPolicy(from: proto)

        #expect(persisted.mode == .unlessStopped)
        #expect(persisted.onFailureMaxRetries == 0)
    }

    @Test("proto UNLESS_STOPPED maps to unless-stopped")
    func protoUnlessStoppedMapsToUnlessStopped() {
        var proto = RestartPolicy()
        proto.mode = .unlessStopped

        let persisted = PersistedRestartPolicy(from: proto)

        #expect(persisted.mode == .unlessStopped)
        #expect(persisted.onFailureMaxRetries == 0)
    }

    @Test("proto NO maps to no")
    func protoNoMapsToNo() {
        var proto = RestartPolicy()
        proto.mode = .no

        let persisted = PersistedRestartPolicy(from: proto)

        #expect(persisted.mode == .no)
        #expect(persisted.onFailureMaxRetries == 0)
    }

    @Test("proto ON_FAILURE maps to onFailure carrying maxRetries")
    func protoOnFailureMapsToOnFailureWithMaxRetries() {
        var proto = RestartPolicy()
        proto.mode = .onFailure
        proto.onFailureMaxRetries = 5

        let persisted = PersistedRestartPolicy(from: proto)

        #expect(persisted.mode == .onFailure)
        #expect(persisted.onFailureMaxRetries == 5)
    }

    @Test("proto UNRECOGNIZED maps to unless-stopped")
    func protoUnrecognizedMapsToUnlessStopped() {
        var proto = RestartPolicy()
        proto.mode = .UNRECOGNIZED(99)

        let persisted = PersistedRestartPolicy(from: proto)

        #expect(persisted.mode == .unlessStopped)
        #expect(persisted.onFailureMaxRetries == 0)
    }

    @Test("legacy info.json without restartPolicy or stoppedByUser decodes with defaults")
    func legacyInfoJSONDecodesWithDefaults() throws {
        // Mirrors an info.json written before this field existed: only
        // `info`, `native`, and `container` are present.
        let legacyJSON = """
            [
                {
                    "info": {
                        "id": "sh.wendy.tests.Legacy",
                        "kind": "native",
                        "status": "stopped",
                        "pid": null
                    },
                    "native": {
                        "directory": "/tmp/sh.wendy.tests.Legacy",
                        "binaryName": "app",
                        "args": []
                    }
                }
            ]
            """

        let apps = try JSONDecoder().decode([WendyApp].self, from: Data(legacyJSON.utf8))
        let app = try #require(apps.first)

        #expect(app.restartPolicy == .default)
        #expect(app.restartPolicy.mode == .unlessStopped)
        #expect(app.stoppedByUser == false)
    }

    @Test("round trip preserves policy and stoppedByUser, drops runtime-only fields")
    func roundTripPreservesPersistedFieldsAndDropsRuntimeOnlyFields() throws {
        var app = WendyApp(
            info: WendyAppInfo(
                id: "sh.wendy.tests.RoundTrip",
                kind: .native,
                status: .stopped,
                pid: nil
            ),
            restartPolicy: PersistedRestartPolicy(mode: .onFailure, onFailureMaxRetries: 7),
            stoppedByUser: true
        )
        // Populate the runtime-only fields to prove they don't leak into the
        // persisted representation.
        app.failureCount = 3
        app.lastRestart = Date()
        app.lastExitCode = 42

        let data = try JSONEncoder().encode(app)

        // Assert on the raw JSON, not just the round-tripped struct, so a
        // decoder that silently ignores unknown keys can't mask a leak.
        let rawObject = try #require(
            try JSONSerialization.jsonObject(with: data) as? [String: Any]
        )
        #expect(rawObject["failureCount"] == nil)
        #expect(rawObject["lastRestart"] == nil)
        #expect(rawObject["lastExitCode"] == nil)
        #expect(rawObject["restartPolicy"] != nil)
        #expect(rawObject["stoppedByUser"] != nil)

        let decoded = try JSONDecoder().decode(WendyApp.self, from: data)
        #expect(
            decoded.restartPolicy
                == PersistedRestartPolicy(mode: .onFailure, onFailureMaxRetries: 7)
        )
        #expect(decoded.stoppedByUser == true)
        #expect(decoded.failureCount == 0)
        #expect(decoded.lastRestart == nil)
        #expect(decoded.lastExitCode == nil)
    }
}
