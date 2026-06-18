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
     top-level `wendy.json.files` are runtime app files, while persistent
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
     Synced-file cleanup is scoped to the exact app ID. Removing one app does
     not delete another app's managed synced-file directory when the other app
     has a similar prefix in its ID.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `removes only the exact app scoped synced file directory`() async throws {
        let appID = Self.appID("exact")
        let siblingAppID = appID + "-sibling"

        try await self.scenario.run(authenticated: false) { cli, agent in
            let agentAddress = agent.machine.address

            do {
                try await cli.sh(Self.createProjectScript(appID: appID))
                try await cli.sh(Self.createProjectScript(appID: siblingAppID))

                try await cli.sh(
                    Self.runDeployCommand(
                        project: Self.projectName(appID),
                        agentAddress: agentAddress
                    )
                ) { result in
                    #expect(result.status.isSuccess)
                }
                try await cli.sh(
                    Self.runDeployCommand(
                        project: Self.projectName(siblingAppID),
                        agentAddress: agentAddress
                    )
                ) { result in
                    #expect(result.status.isSuccess)
                }

                try await cli.sh(
                    "wendy --device \(Self.shellQuote(agentAddress)) device apps remove \(Self.shellQuote(appID)) --force"
                ) { result in
                    #expect(result.status.isSuccess)
                }

                try await agent.sh(
                    Self.privilegedTestCommand("! -e", Self.agentFileSyncDirectory(appID))
                ) { result in
                    #expect(result.status.isSuccess)
                }
                try await agent.sh(
                    Self.privilegedTestCommand("-d", Self.agentFileSyncDirectory(siblingAppID))
                ) { result in
                    #expect(result.status.isSuccess)
                }
            } catch {
                try? await cli.sh(
                    "wendy --device \(Self.shellQuote(agentAddress)) device apps remove \(Self.shellQuote(appID)) --force --delete-volumes"
                )
                try? await cli.sh(
                    "wendy --device \(Self.shellQuote(agentAddress)) device apps remove \(Self.shellQuote(siblingAppID)) --force --delete-volumes"
                )
                throw error
            }

            try await cli.sh(
                "wendy --device \(Self.shellQuote(agentAddress)) device apps remove \(Self.shellQuote(siblingAppID)) --force --delete-volumes"
            ) { result in
                #expect(result.status.isSuccess)
            }
        }
    }

    /**
     Removing an application without `--delete-volumes` removes synced
     runtime app files while preserving persistent volume data for the same
     app. This keeps `wendy.json.files` cleanup independent from durable app
     state cleanup.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `removes synced files while preserving persistent volumes`() async throws {
        let appID = Self.appID("preserve")
        let volumeName = "\(appID)-data"

        try await self.scenario.run(authenticated: false) { cli, agent in
            let agentAddress = agent.machine.address

            do {
                try await cli.sh(
                    Self.createVolumeProjectScript(appID: appID, volumeName: volumeName)
                )

                try await cli.sh(
                    Self.runCommand(project: Self.projectName(appID), agentAddress: agentAddress)
                ) { result in
                    let output = result.normalizedStdout + result.normalizedStderr

                    #expect(result.status.isSuccess)
                    #expect(output.contains("VOLUME:seeded") || output.contains("VOLUME:seed"))
                }

                try await agent.sh(
                    Self.privilegedTestCommand("-d", Self.agentFileSyncDirectory(appID))
                ) {
                    result in
                    #expect(result.status.isSuccess)
                }
                try await agent.sh(
                    Self.privilegedTestCommand("-f", Self.agentVolumeFile(volumeName))
                ) {
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
                try await agent.sh(
                    Self.privilegedTestCommand("-f", Self.agentVolumeFile(volumeName))
                ) { result in
                    #expect(result.status.isSuccess)
                }
                try await agent.sh(
                    "test \"$(\(Self.privilegedCommand("cat", Self.agentVolumeFile(volumeName))))\" = seed"
                ) { result in
                    #expect(result.status.isSuccess)
                }
            } catch {
                try? await cli.sh(
                    "wendy --device \(Self.shellQuote(agentAddress)) device apps remove \(Self.shellQuote(appID)) --force --delete-volumes"
                )
                try? await agent.sh(
                    Self.privilegedCommand("rm -rf", Self.agentVolumeDirectory(volumeName))
                )
                throw error
            }

            try await agent.sh(
                Self.privilegedCommand("rm -rf", Self.agentVolumeDirectory(volumeName))
            )
        }
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

    private static func runCommand(project: String, agentAddress: String) -> String {
        "wendy --device \(Self.shellQuote(agentAddress)) run --prefix \(Self.shellQuote(project))"
    }

    private static func agentVolumeDirectory(_ volumeName: String) -> String {
        "/var/lib/wendy/volumes/\(volumeName)"
    }

    private static func agentVolumeFile(_ volumeName: String) -> String {
        Self.agentVolumeDirectory(volumeName) + "/persist.txt"
    }

    private static func privilegedCommand(_ command: String, _ path: String) -> String {
        let shellCommand = "\(command) \(Self.shellQuote(path))"
        return "if [ \"$(id -u)\" = 0 ]; then \(shellCommand); else sudo \(shellCommand); fi"
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

    private static func createVolumeProjectScript(appID: String, volumeName: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))/config
            cat > \(Self.shellQuote(project))/Dockerfile <<'EOF'
            FROM alpine:3.20
            WORKDIR /work
            RUN mkdir -p /work/config /data
            COPY check.sh /check.sh
            CMD ["/bin/sh", "/check.sh"]
            EOF
            cat > \(Self.shellQuote(project))/check.sh <<'EOF'
            #!/bin/sh
            set -eu
            cat config/message.txt >/dev/null
            if [ -f /data/persist.txt ]; then
              printf 'VOLUME:%s\n' "$(cat /data/persist.txt)"
            else
              printf 'seed' > /data/persist.txt
              printf 'VOLUME:seeded\n'
            fi
            EOF
            printf 'volume cleanup' > \(Self.shellQuote(project))/config/message.txt
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "entitlements": [
                { "type": "persist", "name": "\(volumeName)", "path": "/data" }
              ],
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
