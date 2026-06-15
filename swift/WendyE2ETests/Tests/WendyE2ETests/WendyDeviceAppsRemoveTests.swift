import Foundation
import Testing
import WendyE2ETesting

@Suite
struct `'wendy device apps remove'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy device apps remove`. The output includes the
     command synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `prints command help`() async throws {
        // TODO: implement.
    }

    /**
     `--device` selects the target device hostname and skips discovery and
     pickers. The command does not read or change the saved default device when
     an explicit target is supplied.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `uses explicit device selection without prompting`() async throws {
        // TODO: implement.
    }

    /**
     Without an explicit or configured device in a non-interactive context,
     reports that a device selection is required, emits no prompt escape
     sequences, and performs no device operation.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `reports missing device selection in non-interactive mode`() async throws {
        // TODO: implement.
    }

    /**
     Connection failures, timeouts, and incompatible agent responses produce
     stderr diagnostics and a failure status. Output does not claim that the
     operation succeeded.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `reports unreachable devices without partial success`() async throws {
        // TODO: implement.
    }

    /**
     Removes the named application only after confirmation or `--force`.
     Success output identifies the removed app and any optional cleanup
     performed.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `removes an application after confirmation`() async throws {
        // TODO: implement.
    }

    /**
     `--cleanup` removes the container image and `--delete-volumes` removes
     persistent volumes only for the named app. Omitted cleanup flags leave
     those resources intact.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `honors cleanup and volume deletion flags`() async throws {
        // TODO: implement.
    }

    /**
     Removing an application also removes its app-scoped synced file data from
     the agent. This cleanup happens regardless of `--delete-volumes` because
     top-level `wendy.json.files` are deployment inputs, while persistent
     volumes and other apps remain intact unless their explicit cleanup flags
     are provided.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `removes app scoped synced files when removing an application`() async throws {
        let appID = Self.appID("remove")

        try await self.scenario.run(authenticated: false) { cli, agent in
            let agentAddress = agent.machine.address
            try await cli.sh(Self.createProjectScript(appID: appID))

            try await cli.sh(
                Self.runDeployCommand(project: Self.projectName(appID), agentAddress: agentAddress)
            ) { result in
                let output = result.normalizedStdout + result.normalizedStderr

                #expect(result.status.isSuccess)
                #expect(output.contains("created") || output.contains("deployed"))
            }

            try await agent.sh(Self.privilegedTestCommand("-d", Self.agentFileSyncDirectory(appID)))
            {
                result in
                #expect(result.status.isSuccess)
            }

            try await cli.sh(
                "wendy --device \(Self.shellQuote(agentAddress)) device apps remove \(Self.shellQuote(appID)) --force"
            ) { result in
                let output = result.normalizedStdout + result.normalizedStderr

                #expect(result.status.isSuccess)
                #expect(output.contains("Application \(appID) removed"))
                #expect(!output.contains("Persistent volume deletion requested"))
            }

            try await agent.sh(
                Self.privilegedTestCommand("! -e", Self.agentFileSyncDirectory(appID))
            ) {
                result in
                #expect(result.status.isSuccess)
            }
        }
    }

    /**
     Removing an application without `--delete-volumes` removes synced
     deployment inputs while preserving persistent volume data for the same
     app. This keeps `wendy.json.files` cleanup independent from durable app
     state cleanup.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `removes synced files while preserving persistent volumes`() async throws {
        // TODO: implement.
    }

    /**
     An app name that is not deployed produces a failure diagnostic and does
     not remove images, volumes, or other apps.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `reports unknown applications without deleting resources`() async throws {
        // TODO: implement.
    }

    /**
     Accepts only the documented arguments and flags for `wendy device apps
     remove`. Extra positional arguments or unknown flags produce a usage
     diagnostic on stderr, return a failure status, emit no success output,
     and leave existing state unchanged.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `rejects undocumented arguments and flags`() async throws {
        // TODO: implement.
    }
    // MARK: - File Sync Helpers

    private static func appID(_ suffix: String) -> String {
        "sh.wendy.e2e.filesync.\(suffix).\(UUID().uuidString.lowercased())"
    }

    private static func projectName(_ appID: String) -> String {
        "project-\(appID)"
    }

    private static func agentFileSyncDirectory(_ appID: String) -> String {
        "/var/lib/wendy/files/\(appID)"
    }

    private static func runDeployCommand(project: String, agentAddress: String) -> String {
        "wendy --device \(Self.shellQuote(agentAddress)) run --deploy --prefix \(Self.shellQuote(project))"
    }

    private static func privilegedTestCommand(_ predicate: String, _ path: String) -> String {
        let testCommand = "test \(predicate) \(Self.shellQuote(path))"
        return "if [ \"$(id -u)\" = 0 ]; then \(testCommand); else sudo \(testCommand); fi"
    }

    private static func createProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))/config
            cat > \(Self.shellQuote(project))/Dockerfile <<'EOF'
            FROM alpine:3.20
            WORKDIR /work
            RUN mkdir -p /work/config
            CMD ["/bin/sh", "-c", "cat config/message.txt"]
            EOF
            printf 'remove cleanup' > \(Self.shellQuote(project))/config/message.txt
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "files": [
                { "path": "config/message.txt" }
              ]
            }
            EOF
            """
    }

    private static func shellQuote(_ value: String) -> String {
        "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}
