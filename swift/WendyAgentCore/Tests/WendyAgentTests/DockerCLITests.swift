import Foundation
import Testing

@testable import WendyAgentCore

struct DockerCLITests {
    @Test("docker run options include readonly bind mounts")
    func dockerRunOptionsIncludeReadonlyBindMounts() {
        let arguments = DockerCLI.RunOption.bindReadOnly(
            source: "/tmp/source",
            target: "/app/config.json"
        ).arguments

        #expect(
            arguments == [
                "--mount", "type=bind,source=/tmp/source,target=/app/config.json,readonly",
            ]
        )
    }

    @Test("file sync entries become readonly Docker bind mounts")
    func fileSyncEntriesBecomeReadonlyDockerBindMounts() async throws {
        let appDirectory = try Self.makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: appDirectory) }

        let configURL = appDirectory.appendingPathComponent("config/config.json")
        try FileManager.default.createDirectory(
            at: configURL.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try "{}".write(to: configURL, atomically: true, encoding: .utf8)

        let backend = DockerContainerBackend()
        let options = try await backend.fileSyncMountOptionsForTesting(
            from: [WendyFileSyncConfigEntry(path: "config.json", to: "config/config.json")],
            appDirectory: appDirectory,
            workingDir: "/app"
        )

        #expect(
            options.flatMap(\.arguments) == [
                "--mount",
                "type=bind,source=\(configURL.path),target=/app/config/config.json,readonly",
            ]
        )
    }

    @Test("file sync mount targets clean container working directories")
    func fileSyncMountTargetsCleanContainerWorkingDirectories() async throws {
        let appDirectory = try Self.makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: appDirectory) }

        let payloadURL = appDirectory.appendingPathComponent("payload.txt")
        try "payload".write(to: payloadURL, atomically: true, encoding: .utf8)

        let backend = DockerContainerBackend()
        let cases: [(workingDir: String, target: String)] = [
            ("/app/", "/app/payload.txt"),
            ("app", "/app/payload.txt"),
            ("/app/./worker", "/app/worker/payload.txt"),
            ("/app/../work", "/work/payload.txt"),
            ("/", "/payload.txt"),
        ]

        for testCase in cases {
            let options = try await backend.fileSyncMountOptionsForTesting(
                from: [WendyFileSyncConfigEntry(path: "payload.txt", to: nil)],
                appDirectory: appDirectory,
                workingDir: testCase.workingDir
            )

            #expect(
                options.flatMap(\.arguments) == [
                    "--mount",
                    "type=bind,source=\(payloadURL.path),target=\(testCase.target),readonly",
                ]
            )
        }
    }

    @Test("experimental macOS Linux containers flag accepts explicit opt in")
    func experimentalMacOSLinuxContainersFlagAcceptsExplicitOptIn() {
        #expect(
            WendyAgent.experimentalMacOSLinuxContainersEnabled(environment: [
                "WENDY_EXPERIMENTAL_MACOS_LINUX_CONTAINERS": "1"
            ])
        )
        #expect(
            WendyAgent.experimentalMacOSLinuxContainersEnabled(environment: [
                "WENDY_EXPERIMENTAL_MACOS_LINUX_CONTAINERS": "true"
            ])
        )
        #expect(!WendyAgent.experimentalMacOSLinuxContainersEnabled(environment: [:]))
        #expect(
            !WendyAgent.experimentalMacOSLinuxContainersEnabled(environment: [
                "WENDY_EXPERIMENTAL_MACOS_LINUX_CONTAINERS": "false"
            ])
        )
    }

    @Test("checkAvailability returns false when the docker probe times out")
    func checkAvailabilityReturnsFalseWhenProbeTimesOut() async throws {
        let scriptURL = try Self.makeExecutableScript(
            name: "fake-docker-timeout.sh",
            contents: """
                #!/bin/sh
                sleep 1
                exit 0
                """
        )
        defer { try? FileManager.default.removeItem(at: scriptURL.deletingLastPathComponent()) }

        let docker = DockerCLI(
            executable: scriptURL.path,
            startupCommandTimeout: .milliseconds(100)
        )

        let availability = await docker.checkAvailability()

        #expect(availability.isAvailable == false)
        #expect(availability.failureMessage?.contains("timed out") == true)
    }

    @Test("checkAvailability returns true when the docker probe completes")
    func checkAvailabilityReturnsTrueWhenProbeCompletes() async throws {
        let scriptURL = try Self.makeExecutableScript(
            name: "fake-docker-ok.sh",
            contents: """
                #!/bin/sh
                echo 27.0.1
                exit 0
                """
        )
        defer { try? FileManager.default.removeItem(at: scriptURL.deletingLastPathComponent()) }

        let docker = DockerCLI(
            executable: scriptURL.path,
            startupCommandTimeout: .seconds(2)
        )

        let availability = await docker.checkAvailability()

        #expect(availability.isAvailable == true)
        #expect(availability.failureMessage == nil)
    }

    @Test("checkAvailability reports the paths searched when docker is missing")
    func checkAvailabilityReportsSearchedPathsWhenDockerIsMissing() async throws {
        let executable = "missing-docker-\(UUID().uuidString)"
        let docker = DockerCLI(
            executable: executable,
            startupCommandTimeout: .milliseconds(100),
            environment: ["PATH": "/tmp:/bin"]
        )

        let availability = await docker.checkAvailability()
        let resolution = docker.resolveExecutableForTesting()

        #expect(availability.isAvailable == false)
        #expect(
            availability.failureMessage?.contains("Could not find \(executable) executable") == true
        )
        #expect(availability.failureMessage?.contains("/tmp/\(executable)") == true)
        #expect(
            availability.failureMessage?.contains(
                "/Applications/Docker.app/Contents/Resources/bin/\(executable)"
            ) == true
        )
        #expect(resolution.resolvedPath == nil)
        #expect(resolution.searchedPaths.contains("/tmp/\(executable)"))
        #expect(
            resolution.searchedPaths.contains(
                "/Applications/Docker.app/Contents/Resources/bin/\(executable)"
            )
        )
    }

    private static func makeTempDirectory() throws -> URL {
        let directoryURL = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: directoryURL, withIntermediateDirectories: true)
        return directoryURL
    }

    private static func makeExecutableScript(name: String, contents: String) throws -> URL {
        let directoryURL = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: directoryURL, withIntermediateDirectories: true)

        let scriptURL = directoryURL.appendingPathComponent(name)
        try contents.write(to: scriptURL, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes(
            [.posixPermissions: 0o755],
            ofItemAtPath: scriptURL.path
        )
        return scriptURL
    }
}
