import Foundation
import Testing
import WendyE2ETesting

@Suite
struct `'wendy run'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy run`. The output includes the command synopsis,
     local flags, inherited global flags, and concise descriptions. Help exits
     successfully, writes to stdout, emits no stderr, and leaves configuration,
     cache, project, cloud, and device state untouched.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `prints command help`() async throws {
        // TODO: implement.
    }

    /**
     Reads the project configuration, builds the application image,
     deploys it over the selected direct device connection, and starts the
     container. Success output makes the running app and target device
     clear.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `builds, deploys, and starts the current project`() async throws {
        // TODO: implement.
    }

    /**
     Synchronizes top-level `wendy.json.files` before creating a Linux
     container. A declared file with no `to` value appears under the container
     working directory at its relative `path`, and a declared directory with a
     relative `to` value appears at that remapped destination with its nested
     contents intact.

     The app observes the synced content on first start, and success output
     still describes the normal build, deploy, and start result rather than
     exposing host paths.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `syncs configured files into a Linux container`() async throws {
        let appID = Self.appID("sync")

        try await self.scenario.run(authenticated: false) { cli, agent in
            let agentAddress = agent.machine.address
            try await Self.withRemovedApp(appID, cli: cli, agentAddress: agentAddress) {
                try await cli.sh(Self.createSyncProjectScript(appID: appID))

                try await cli.sh(
                    Self.runCommand(project: Self.projectName(appID), agentAddress: agentAddress)
                ) { result in
                    let output = result.normalizedStdout + result.normalizedStderr

                    #expect(result.status.isSuccess)
                    #expect(output.contains("SYNC_FILE:hello from file sync"))
                    #expect(output.contains("SYNC_DIR:nested asset"))
                    #expect(!output.contains("/var/lib/wendy/files"))
                }
            }
        }
    }

    /**
     A configured file can be mounted at a destination different from its
     project-relative source path. The `to` value is interpreted relative to
     the container working directory, the source path name does not leak into
     the destination, and the file contents match the local project file.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `supports configured file destinations with to`() async throws {
        let appID = Self.appID("to")

        try await self.scenario.run(authenticated: false) { cli, agent in
            let agentAddress = agent.machine.address
            try await Self.withRemovedApp(appID, cli: cli, agentAddress: agentAddress) {
                try await cli.sh(Self.createToProjectScript(appID: appID))

                try await cli.sh(
                    Self.runCommand(project: Self.projectName(appID), agentAddress: agentAddress)
                ) { result in
                    let output = result.normalizedStdout + result.normalizedStderr

                    #expect(result.status.isSuccess)
                    #expect(output.contains("TO:remapped destination"))
                    #expect(output.contains("SOURCE_PATH:absent"))
                }
            }
        }
    }

    /**
     Re-running a project with changed `wendy.json.files` updates the
     app-scoped file-sync area before the replacement container starts. Updated
     files are visible with their new contents and mode, removed declarations
     are pruned from the managed app file-sync area, and unrelated app data such
     as persistent volumes remains untouched.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `updates synced files and prunes stale paths on redeploy`() async throws {
        let appID = Self.appID("redeploy")

        try await self.scenario.run(authenticated: false) { cli, agent in
            let agentAddress = agent.machine.address
            try await Self.withRemovedApp(
                appID,
                cli: cli,
                agentAddress: agentAddress,
                deleteVolumes: true
            ) {
                try await cli.sh(Self.createRedeployProjectScript(appID: appID))

                try await cli.sh(
                    Self.runCommand(project: Self.projectName(appID), agentAddress: agentAddress)
                ) { result in
                    let output = result.normalizedStdout + result.normalizedStderr

                    #expect(result.status.isSuccess)
                    #expect(output.contains("MSG:v1"))
                    #expect(output.contains("OLD:present"))
                    #expect(output.contains("PERSIST:seeded"))
                }

                try await cli.sh(Self.updateRedeployProjectScript(appID: appID))

                try await cli.sh(
                    Self.runCommand(project: Self.projectName(appID), agentAddress: agentAddress)
                ) { result in
                    let output = result.normalizedStdout + result.normalizedStderr

                    #expect(result.status.isSuccess)
                    #expect(output.contains("MSG:v2"))
                    #expect(output.contains("OLD:absent"))
                    #expect(output.contains("PERSIST:seed\n"))
                    #expect(!output.contains("PERSIST:seeded"))
                }
            }
        }
    }

    /**
     Files and directories declared in top-level `wendy.json.files` are mounted
     read-only into Linux containers. An app that attempts to overwrite or
     remove a declared file receives a filesystem failure, and neither the
     original project file nor the agent-managed synced copy is mutated by the
     running container.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `mounts synced files read-only in the container`() async throws {
        let appID = Self.appID("readonly")

        try await self.scenario.run(authenticated: false) { cli, agent in
            let agentAddress = agent.machine.address
            try await Self.withRemovedApp(appID, cli: cli, agentAddress: agentAddress) {
                try await cli.sh(Self.createReadOnlyProjectScript(appID: appID))

                try await cli.sh(
                    Self.runCommand(project: Self.projectName(appID), agentAddress: agentAddress)
                ) { result in
                    let output = result.normalizedStdout + result.normalizedStderr

                    #expect(result.status.isSuccess)
                    #expect(output.contains("WRITE:denied"))
                    #expect(output.contains("REMOVE:denied"))
                    #expect(output.contains("CONTENT:immutable"))
                }

                let sourcePath = Self.projectName(appID) + "/config/message.txt"
                try await cli.sh("test \"$(cat \(Self.shellQuote(sourcePath)))\" = immutable") {
                    result in
                    #expect(result.status.isSuccess)
                }
            }
        }
    }

    /**
     A configured source path that does not exist fails before deployment. The
     diagnostic identifies the missing path, the command exits unsuccessfully,
     and output does not report that an image, container, or app was deployed.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `rejects missing configured files before deployment`() async throws {
        let appID = Self.appID("missing")

        try await self.scenario.run(authenticated: false) { cli, agent in
            try await cli.sh(Self.createMissingFileProjectScript(appID: appID))

            try await cli.sh(
                Self.runCommand(
                    project: Self.projectName(appID),
                    agentAddress: agent.machine.address
                )
            ) { result in
                let output = result.normalizedStdout + result.normalizedStderr

                #expect(result.status.isFailure)
                #expect(output.contains("files[0]"))
                #expect(output.contains("missing.txt"))
                #expect(!output.contains("Building and pushing Docker image"))
                #expect(!output.contains("Container \(appID) created"))
                #expect(!output.contains("Application \(appID) started"))
            }
        }
    }

    /**
     Unsafe file-sync source paths are rejected before deployment. Absolute
     `path` values and values containing `..` produce an actionable
     configuration diagnostic, return a failure status, and do not build an
     image, create a container, or write outside the app-scoped file-sync area.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `rejects unsafe configured file paths before deployment`() async throws {
        let appID = Self.appID("unsafe")

        try await self.scenario.run(authenticated: false) { cli, agent in
            try await cli.sh(Self.createUnsafeProjectScript(appID: appID))

            try await cli.sh(
                Self.runCommand(
                    project: Self.projectName(appID),
                    agentAddress: agent.machine.address
                )
            ) { result in
                let output = result.normalizedStdout + result.normalizedStderr

                #expect(result.status.isFailure)
                #expect(output.contains("files[0]"))
                #expect(output.contains("path must not contain '..'"))
                #expect(!output.contains("Building and pushing Docker image"))
                #expect(!output.contains("Creating container"))
            }
        }
    }

    /**
     Unsafe file-sync destinations are rejected before deployment. Absolute
     `to` values, empty destinations, and values containing `..` produce an
     actionable configuration diagnostic, return a failure status, and do not
     build an image, create a container, or write outside the app-scoped
     file-sync area.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `rejects unsafe configured file destinations before deployment`() async throws {
        let appID = Self.appID("unsafeto")

        try await self.scenario.run(authenticated: false) { cli, agent in
            try await cli.sh(Self.createUnsafeDestinationProjectScript(appID: appID))

            try await cli.sh(
                Self.runCommand(
                    project: Self.projectName(appID),
                    agentAddress: agent.machine.address
                )
            ) { result in
                let output = result.normalizedStdout + result.normalizedStderr

                #expect(result.status.isFailure)
                #expect(output.contains("files[0]"))
                #expect(output.contains("to must not contain '..'"))
                #expect(!output.contains("Building and pushing Docker image"))
                #expect(!output.contains("Creating container"))
            }
        }
    }

    /**
     Duplicate effective destinations are rejected before deployment. If two
     `wendy.json.files` entries would populate the same container-relative
     destination, the command reports the conflict instead of choosing an
     arbitrary winner or producing nondeterministic mounts.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `rejects duplicate configured file destinations`() async throws {
        let appID = Self.appID("duplicate")

        try await self.scenario.run(authenticated: false) { cli, agent in
            try await cli.sh(Self.createDuplicateDestinationProjectScript(appID: appID))

            try await cli.sh(
                Self.runCommand(
                    project: Self.projectName(appID),
                    agentAddress: agent.machine.address
                )
            ) { result in
                let output = result.normalizedStdout + result.normalizedStderr

                #expect(result.status.isFailure)
                #expect(output.contains("files[1]"))
                #expect(output.contains("destination"))
                #expect(output.contains("conflicts with files[0]"))
                #expect(!output.contains("Building and pushing Docker image"))
                #expect(!output.contains("Creating container"))
            }
        }
    }

    /**
     The container runtime mounts synced files from the agent-managed
     app-scoped file-sync area, not directly from the original CLI project
     path. The observable deployment path is project file -> agent sync area ->
     read-only container mount, which prevents arbitrary CLI host path mounts.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `mounts configured files from the agent managed sync area`() async throws {
        let appID = Self.appID("managed")

        try await self.scenario.run(authenticated: false) { cli, agent in
            let agentAddress = agent.machine.address
            try await Self.withRemovedApp(appID, cli: cli, agentAddress: agentAddress) {
                try await cli.sh(Self.createManagedMountProjectScript(appID: appID))

                try await cli.sh(
                    Self.runDeployCommand(
                        project: Self.projectName(appID),
                        agentAddress: agentAddress
                    )
                ) { result in
                    let output = result.normalizedStdout + result.normalizedStderr

                    #expect(result.status.isSuccess)
                    #expect(output.contains("Container \(appID) created"))
                }

                let project = Self.projectName(appID)
                let expectedSource = Self.agentFileSyncPath(appID, "config/message.txt")
                try await agent.sh(
                    Self.privilegedShell(
                        "ctr -n default containers info \(Self.shellQuote(appID))"
                    )
                ) { result in
                    let output = result.normalizedStdout + result.normalizedStderr

                    #expect(result.status.isSuccess)
                    #expect(output.contains(expectedSource))
                    #expect(!output.contains(project))
                }
            }
        }
    }

    /**
     Multi-service deployments do not consume top-level `wendy.json.files` yet.
     A project that combines services with top-level file declarations reports
     the unsupported combination clearly instead of silently ignoring files or
     mounting them into only one service.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `reports top-level files as unsupported for multi-service deployments`() async throws {
        let appID = Self.appID("multiservice")

        try await self.scenario.run(authenticated: false) { cli, agent in
            try await cli.sh(Self.createMultiServiceProjectScript(appID: appID))

            try await cli.sh(
                Self.runCommand(
                    project: Self.projectName(appID),
                    agentAddress: agent.machine.address
                )
            ) { result in
                let output = result.normalizedStdout + result.normalizedStderr

                #expect(result.status.isFailure)
                #expect(output.contains("top-level wendy.json files"))
                #expect(output.contains("multi-service"))
                #expect(!output.contains("Building service"))
                #expect(!output.contains("Creating container"))
            }
        }
    }

    /**
     Compose deployments do not consume top-level `wendy.json.files` yet. A
     project that combines Compose configuration with top-level file
     declarations reports the unsupported combination clearly instead of
     silently ignoring files or inventing Compose volume semantics.
     */
    @Test(.enabled(if: isAgentLinuxOrWendyOS))
    func `reports top-level files as unsupported for compose deployments`() async throws {
        let appID = Self.appID("compose")

        try await self.scenario.run(authenticated: false) { cli, agent in
            try await cli.sh(Self.createComposeFilesProjectScript(appID: appID))

            try await cli.sh(
                Self.runCommand(
                    project: Self.projectName(appID),
                    agentAddress: agent.machine.address
                )
            ) { result in
                let output = result.normalizedStdout + result.normalizedStderr

                #expect(result.status.isFailure)
                #expect(output.contains("top-level wendy.json files"))
                #expect(output.contains("Compose"))
                #expect(!output.contains("Building image for service"))
                #expect(!output.contains("Creating container"))
            }
        }
    }

    /**
     Linux-container deployment from macOS remains unsupported by default. If
     `WENDY_EXPERIMENTAL_MACOS_LINUX_CONTAINERS` is not set, `wendy run`
     returns a clear failure and mentions the experimental opt-in flag rather
     than probing Docker or attempting a container deployment.
     */
    @Test(.enabled(if: isAgentMacOS && WendyE2EEnvironment.agentAddress != nil))
    func `keeps macOS Linux container support behind the experiment flag`() async throws {
        let appID = Self.appID("maclinux")

        try await self.scenario.run(authenticated: false) { cli, agent in
            try await cli.sh(Self.createMacOSLinuxContainerProjectScript(appID: appID))

            try await cli.sh(
                Self.runCommandWithoutMacOSLinuxContainerExperiment(
                    project: Self.projectName(appID),
                    agentAddress: agent.machine.address
                )
            ) { result in
                let output = result.normalizedStdout + result.normalizedStderr

                #expect(result.status.isFailure)
                #expect(output.contains("Linux containers aren't supported on Macs yet"))
                #expect(output.contains("WENDY_EXPERIMENTAL_MACOS_LINUX_CONTAINERS=1"))
                #expect(!output.contains("Building and pushing Docker image"))
                #expect(!output.contains("Creating container"))
            }
        }
    }

    /**
     `--deploy` creates or updates the container on the target device and
     leaves it stopped. The command exits successfully after deployment and
     prints no live log stream.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `deploys without starting when requested`() async throws {
        // TODO: implement.
    }

    /**
     `--detach` starts the application and returns after start-up status is
     known. Output includes the app name and how to view logs later.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `detaches after starting when requested`() async throws {
        // TODO: implement.
    }

    /**
     `--user-args` preserves argument boundaries and forwards the provided
     values to the started application without interpreting secrets or shell
     metacharacters locally.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `passes user arguments to the container`() async throws {
        // TODO: implement.
    }

    /**
     `--prefix` selects the project directory and `--device` selects the target
     device and skips the picker. The command does not read unrelated
     `wendy.json` files or open interactive device selection.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `uses explicit project and device selection`() async throws {
        // TODO: implement.
    }

    /**
     Build failures, invalid project configuration, unreachable devices, or
     deployment errors return a failure status. Partial remote resources are
     either cleaned up or identified clearly for manual cleanup.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `reports build or deployment failure without claiming success`() async throws {
        // TODO: implement.
    }

    /**
     With `--json`, emits structured build, deploy, start, and app metadata.
     Progress and streamed container logs do not corrupt stdout JSON.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `prints JSON run metadata for automation`() async throws {
        // TODO: implement.
    }
    // MARK: - File Sync Helpers

    private static func withRemovedApp(
        _ appID: String,
        cli: WendyE2ESession,
        agentAddress: String,
        deleteVolumes: Bool = false,
        _ body: () async throws -> Void
    ) async throws {
        do {
            try await body()
        } catch {
            await Self.removeApp(
                appID,
                cli: cli,
                agentAddress: agentAddress,
                deleteVolumes: deleteVolumes
            )
            throw error
        }

        await Self.removeApp(
            appID,
            cli: cli,
            agentAddress: agentAddress,
            deleteVolumes: deleteVolumes
        )
    }

    private static func removeApp(
        _ appID: String,
        cli: WendyE2ESession,
        agentAddress: String,
        deleteVolumes: Bool
    ) async {
        let deleteVolumesFlag = deleteVolumes ? " --delete-volumes" : ""
        try? await cli.sh(
            "wendy --device \(Self.shellQuote(agentAddress)) device apps remove \(Self.shellQuote(appID)) --force\(deleteVolumesFlag)"
        )
    }

    private static func appID(_ suffix: String) -> String {
        "sh.wendy.e2e.filesync.\(suffix).\(UUID().uuidString.lowercased())"
    }

    private static func projectName(_ appID: String) -> String {
        "project-\(appID)"
    }

    private static func runCommand(project: String, agentAddress: String) -> String {
        "wendy --device \(Self.shellQuote(agentAddress)) run --prefix \(Self.shellQuote(project))"
    }

    private static func runDeployCommand(project: String, agentAddress: String) -> String {
        "wendy --device \(Self.shellQuote(agentAddress)) run --deploy --prefix \(Self.shellQuote(project))"
    }

    private static func runCommandWithoutMacOSLinuxContainerExperiment(
        project: String,
        agentAddress: String
    ) -> String {
        "env -u WENDY_EXPERIMENTAL_MACOS_LINUX_CONTAINERS \(Self.runCommand(project: project, agentAddress: agentAddress))"
    }

    private static func agentFileSyncPath(_ appID: String, _ path: String) -> String {
        "/var/lib/wendy/files/\(appID)/\(path)"
    }

    private static func privilegedShell(_ command: String) -> String {
        "if [ \"$(id -u)\" = 0 ]; then \(command); else sudo \(command); fi"
    }

    private static func createSyncProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))/config \(Self.shellQuote(project))/fixtures/nested
            cat > \(Self.shellQuote(project))/Dockerfile <<'EOF'
            FROM alpine:3.20
            WORKDIR /work
            RUN mkdir -p /work/config /work/mounted/assets/nested
            COPY check.sh /check.sh
            CMD ["/bin/sh", "/check.sh"]
            EOF
            cat > \(Self.shellQuote(project))/check.sh <<'EOF'
            #!/bin/sh
            set -eu
            test "$(cat config/message.txt)" = "hello from file sync"
            test "$(cat mounted/assets/nested/value.txt)" = "nested asset"
            printf 'SYNC_FILE:%s\n' "$(cat config/message.txt)"
            printf 'SYNC_DIR:%s\n' "$(cat mounted/assets/nested/value.txt)"
            EOF
            printf 'hello from file sync' > \(Self.shellQuote(project))/config/message.txt
            printf 'nested asset' > \(Self.shellQuote(project))/fixtures/nested/value.txt
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "files": [
                { "path": "config/message.txt" },
                { "path": "fixtures", "to": "mounted/assets" }
              ]
            }
            EOF
            """
    }

    private static func createToProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))/local
            cat > \(Self.shellQuote(project))/Dockerfile <<'EOF'
            FROM alpine:3.20
            WORKDIR /work
            RUN mkdir -p /work/config
            COPY check.sh /check.sh
            CMD ["/bin/sh", "/check.sh"]
            EOF
            cat > \(Self.shellQuote(project))/check.sh <<'EOF'
            #!/bin/sh
            set -eu
            test "$(cat config/app.json)" = "remapped destination"
            if [ -e local/config.json ]; then
              printf 'SOURCE_PATH:present\n'
              exit 1
            fi
            printf 'TO:%s\n' "$(cat config/app.json)"
            printf 'SOURCE_PATH:absent\n'
            EOF
            printf 'remapped destination' > \(Self.shellQuote(project))/local/config.json
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "files": [
                { "path": "local/config.json", "to": "config/app.json" }
              ]
            }
            EOF
            """
    }

    private static func createRedeployProjectScript(appID: String) -> String {
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
            if [ -f /data/persist.txt ]; then
              printf 'PERSIST:%s\n' "$(cat /data/persist.txt)"
            else
              printf 'seed' > /data/persist.txt
              printf 'PERSIST:seeded\n'
            fi
            if [ -e config/old.txt ]; then
              printf 'OLD:present\n'
            else
              printf 'OLD:absent\n'
            fi
            printf 'MSG:%s\n' "$(cat config/message.txt)"
            EOF
            printf 'v1' > \(Self.shellQuote(project))/config/message.txt
            printf 'stale' > \(Self.shellQuote(project))/config/old.txt
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "entitlements": [
                { "type": "persist", "name": "\(appID)-data", "path": "/data" }
              ],
              "files": [
                { "path": "config/message.txt" },
                { "path": "config/old.txt" }
              ]
            }
            EOF
            """
    }

    private static func updateRedeployProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            printf 'v2' > \(Self.shellQuote(project))/config/message.txt
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "entitlements": [
                { "type": "persist", "name": "\(appID)-data", "path": "/data" }
              ],
              "files": [
                { "path": "config/message.txt" }
              ]
            }
            EOF
            """
    }

    private static func createReadOnlyProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))/config
            cat > \(Self.shellQuote(project))/Dockerfile <<'EOF'
            FROM alpine:3.20
            WORKDIR /work
            RUN mkdir -p /work/config
            COPY check.sh /check.sh
            CMD ["/bin/sh", "/check.sh"]
            EOF
            cat > \(Self.shellQuote(project))/check.sh <<'EOF'
            #!/bin/sh
            set -eu
            if echo changed > config/message.txt 2>/tmp/write.err; then
              printf 'WRITE:unexpected\n'
              exit 1
            else
              printf 'WRITE:denied\n'
            fi
            if rm config/message.txt 2>/tmp/remove.err; then
              printf 'REMOVE:unexpected\n'
              exit 1
            else
              printf 'REMOVE:denied\n'
            fi
            printf 'CONTENT:%s\n' "$(cat config/message.txt)"
            EOF
            printf 'immutable' > \(Self.shellQuote(project))/config/message.txt
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

    private static func createMissingFileProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))
            cat > \(Self.shellQuote(project))/Dockerfile <<'EOF'
            FROM alpine:3.20
            CMD ["/bin/sh", "-c", "echo should-not-run"]
            EOF
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "files": [
                { "path": "missing.txt" }
              ]
            }
            EOF
            """
    }

    private static func createUnsafeProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))
            cat > \(Self.shellQuote(project))/Dockerfile <<'EOF'
            FROM alpine:3.20
            CMD ["/bin/sh", "-c", "echo should-not-run"]
            EOF
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "files": [
                { "path": "../outside.txt" }
              ]
            }
            EOF
            """
    }

    private static func createUnsafeDestinationProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))/config
            cat > \(Self.shellQuote(project))/Dockerfile <<'EOF'
            FROM alpine:3.20
            CMD ["/bin/sh", "-c", "echo should-not-run"]
            EOF
            printf 'secret' > \(Self.shellQuote(project))/config/message.txt
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "files": [
                { "path": "config/message.txt", "to": "../message.txt" }
              ]
            }
            EOF
            """
    }

    private static func createDuplicateDestinationProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))/config
            cat > \(Self.shellQuote(project))/Dockerfile <<'EOF'
            FROM alpine:3.20
            CMD ["/bin/sh", "-c", "echo should-not-run"]
            EOF
            printf 'one' > \(Self.shellQuote(project))/config/one.txt
            printf 'two' > \(Self.shellQuote(project))/config/two.txt
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "files": [
                { "path": "config/one.txt", "to": "config/message.txt" },
                { "path": "config/two.txt", "to": "config/message.txt" }
              ]
            }
            EOF
            """
    }

    private static func createManagedMountProjectScript(appID: String) -> String {
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
            printf 'managed mount' > \(Self.shellQuote(project))/config/message.txt
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

    private static func createMultiServiceProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))/api \(Self.shellQuote(project))/config
            cat > \(Self.shellQuote(project))/api/Dockerfile <<'EOF'
            FROM alpine:3.20
            CMD ["/bin/sh", "-c", "echo should-not-build"]
            EOF
            printf 'shared' > \(Self.shellQuote(project))/config/shared.txt
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "files": [
                { "path": "config/shared.txt" }
              ],
              "services": {
                "api": { "context": "api" }
              }
            }
            EOF
            """
    }

    private static func createComposeFilesProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))/api \(Self.shellQuote(project))/config
            cat > \(Self.shellQuote(project))/api/Dockerfile <<'EOF'
            FROM alpine:3.20
            CMD ["/bin/sh", "-c", "echo should-not-build"]
            EOF
            cat > \(Self.shellQuote(project))/compose.yml <<'EOF'
            services:
              api:
                build: ./api
            EOF
            printf 'shared' > \(Self.shellQuote(project))/config/shared.txt
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "files": [
                { "path": "config/shared.txt" }
              ]
            }
            EOF
            """
    }

    private static func createMacOSLinuxContainerProjectScript(appID: String) -> String {
        let project = Self.projectName(appID)
        return """
            set -eu
            rm -rf \(Self.shellQuote(project))
            mkdir -p \(Self.shellQuote(project))
            cat > \(Self.shellQuote(project))/Dockerfile <<'EOF'
            FROM alpine:3.20
            CMD ["/bin/sh", "-c", "echo should-not-run"]
            EOF
            cat > \(Self.shellQuote(project))/wendy.json <<'EOF'
            {
              "appId": "\(appID)",
              "platform": "linux/amd64"
            }
            EOF
            """
    }

    private static func shellQuote(_ value: String) -> String {
        "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}
