import Foundation
import Testing
import WendyE2ETesting

private typealias Machine = WendyE2EMachine

@Suite(.enabled(if: Machine.agent.os == .macOS, "Specs only valid for macOS agent"))
struct `'wendy run' with native Mac Brewfiles` {
    /**
     Auto-detects `Brewfile.wendy`, syncs it to the target Mac, and runs
     target-side `brew bundle` before the app starts.
     */
    @Test
    func `syncs the 'Brewfile.wendy' and runs 'brew bundle' on the target before starting`()
        async throws
    {
        try await Self.helloFormulaScenario().run(authenticated: false) { cli, agent in
            let project = try await Self.makeSwiftPMProject(
                cli,
                name: "swiftpm-autodetect",
                appID: "sh.wendy.e2e.brewfile.swiftpm.autodetect",
                appSource: Self.swiftHelloAppSource(marker: "SWIFTPM_AUTODETECT"),
                extraFiles: ["Brewfile.wendy": Self.helloBrewfile]
            )

            let result = try await Self.wendyRun(cli, agent: agent, project: project)
            #expect(result.status.isSuccess)
            #expect(result.stdout.contains("Will apply Brewfile on target Mac."))
            #expect(result.stdout.contains("Brewfile applied."))
            #expect(result.stdout.contains("SWIFTPM_AUTODETECT: Hello, world!"))
        }
    }

    /**
     Applies the same Brewfile behavior for native Xcode projects. The test uses
     a tiny fake `xcodebuild` to exercise Wendy's Xcode path while still using
     the real target-side Homebrew installation.
     */
    @Test
    func `syncs 'Brewfile.wendy' and runs 'brew bundle' for Xcode apps`() async throws {
        try await Self.helloFormulaScenario().run(authenticated: false) { cli, agent in
            try await Self.installFakeXcodebuild(cli, scheme: "BrewfileXcode")
            let project = try await Self.makeXcodeProject(
                cli,
                name: "xcode-autodetect",
                appID: "sh.wendy.e2e.brewfile.xcode.autodetect",
                extraFiles: ["Brewfile.wendy": Self.helloBrewfile]
            )

            let result = try await Self.wendyRun(cli, agent: agent, project: project)
            #expect(result.status.isSuccess)
            #expect(result.stdout.contains("Will apply Brewfile on target Mac."))
            #expect(result.stdout.contains("Brewfile applied."))
            #expect(result.stdout.contains("XCODE_HELLO: Hello, world!"))
        }
    }

    /**
     Leaves a project-root `Brewfile` for developer-machine setup unless the
     project explicitly opts into using it for the target.
     */
    @Test
    func `does not auto-apply a plain project root 'Brewfile'`() async throws {
        try await CLIAndAgentScenario().run(authenticated: false) { cli, agent in
            let project = try await Self.makeSwiftPMProject(
                cli,
                name: "plain-brewfile-ignored",
                appID: "sh.wendy.e2e.brewfile.plain.ignored",
                appSource: Self.swiftMarkerAppSource(marker: "PLAIN_BREWFILE_IGNORED"),
                extraFiles: ["Brewfile": "brew \"wendy-e2e-this-formula-should-not-exist\"\n"]
            )

            let result = try await Self.wendyRun(cli, agent: agent, project: project)
            #expect(result.status.isSuccess)
            #expect(!result.stdout.contains("Will apply Brewfile"))
            #expect(!result.stdout.contains("Brewfile applied."))
            #expect(result.stdout.contains("PLAIN_BREWFILE_IGNORED"))
        }
    }

    /**
     Uses the explicit `wendy.json` > `brewfile` path and ignores auto-detected
     `Brewfile.wendy` when both are present.
     */
    @Test
    func `uses explicit brewfile path instead of Brewfile_wendy auto detection`() async throws {
        try await Self.helloFormulaScenario().run(authenticated: false) { cli, agent in
            let project = try await Self.makeSwiftPMProject(
                cli,
                name: "explicit-brewfile",
                appID: "sh.wendy.e2e.brewfile.explicit",
                appSource: Self.swiftHelloAppSource(marker: "EXPLICIT_BREWFILE"),
                brewfile: "ops/Brewfile",
                extraFiles: [
                    "Brewfile.wendy": "brew \"wendy-e2e-this-formula-should-not-exist\"\n",
                    "ops/Brewfile": Self.helloBrewfile,
                ]
            )

            let result = try await Self.wendyRun(cli, agent: agent, project: project)
            #expect(result.status.isSuccess)
            #expect(result.stdout.contains("Will apply Brewfile on target Mac."))
            #expect(result.stdout.contains("Brewfile applied."))
            #expect(result.stdout.contains("EXPLICIT_BREWFILE: Hello, world!"))
        }
    }

    /**
     Requires a disposable Mac agent image where Homebrew is absent. We do not
     mutate a real developer/CI Homebrew installation to simulate this.
     */
    @Test(.disabled("requires a disposable macOS agent without Homebrew installed"))
    func `reports missing 'brew' on the target without starting the app`() async throws {
        // Covered by WendyAgentCore unit tests until a no-Homebrew Mac image is available.
    }

    /**
     Fails before app start when target-side `brew bundle` fails.
     */
    @Test
    func `reports 'brew bundle' failures without starting the app`() async throws {
        try await Self.brewRequiredScenario().run(authenticated: false) { cli, agent in
            let project = try await Self.makeSwiftPMProject(
                cli,
                name: "failing-brewfile",
                appID: "sh.wendy.e2e.brewfile.failure",
                appSource: Self.swiftMarkerAppSource(marker: "SHOULD_NOT_START_AFTER_BREW_FAILURE"),
                extraFiles: ["Brewfile.wendy": "brew \"wendy-e2e-this-formula-should-not-exist\"\n"]
            )

            let result = try await Self.wendyRun(cli, agent: agent, project: project)
            #expect(result.status.isFailure)
            #expect(result.stderr.contains("brew bundle failed"))
            #expect(result.stderr.contains("exit code"))
            #expect(!result.stderr.contains("wendy-e2e-this-formula-should-not-exist"))
            #expect(!result.stderr.contains("No available formula"))
            #expect(!result.stdout.contains("Brewfile applied."))
            #expect(!result.stdout.contains("SHOULD_NOT_START_AFTER_BREW_FAILURE"))
            #expect(!result.stderr.contains("SHOULD_NOT_START_AFTER_BREW_FAILURE"))
        }
    }

    /**
     Running the same Brewfile twice should be safe: the first run installs the
     formula, and the second run treats the already-installed dependency as
     satisfied before starting the app again.
     */
    @Test
    func `is idempotent when 'brew bundle' dependencies are already installed`() async throws {
        try await Self.helloFormulaScenario().run(authenticated: false) { cli, agent in
            let project = try await Self.makeSwiftPMProject(
                cli,
                name: "idempotent-brewfile",
                appID: "sh.wendy.e2e.brewfile.idempotent",
                appSource: Self.swiftHelloAppSource(marker: "IDEMPOTENT_BREWFILE"),
                extraFiles: ["Brewfile.wendy": Self.helloBrewfile]
            )

            let first = try await Self.wendyRun(cli, agent: agent, project: project)
            #expect(first.status.isSuccess)
            #expect(first.stdout.contains("IDEMPOTENT_BREWFILE: Hello, world!"))
            #expect(first.stdout.contains("Brewfile applied."))

            let second = try await Self.wendyRun(cli, agent: agent, project: project)
            #expect(second.status.isSuccess)
            #expect(second.stdout.contains("IDEMPOTENT_BREWFILE: Hello, world!"))
            #expect(second.stdout.contains("Brewfile applied."))
        }
    }

    /**
     Rejects ambiguous sync configuration before target-side Brewfile work can
     run.
     */
    @Test
    func `rejects conflicting brewfile file mappings`() async throws {
        try await CLIAndAgentScenario().run(authenticated: false) { cli, agent in
            let project = try await Self.makeSwiftPMProject(
                cli,
                name: "conflicting-brewfile-sync",
                appID: "sh.wendy.e2e.brewfile.conflict",
                appSource: Self.swiftMarkerAppSource(marker: "SHOULD_NOT_START_AFTER_CONFLICT"),
                brewfile: "ops/Brewfile",
                filesJSON: #"[{"path":"dev/Brewfile","to":"ops/Brewfile"}]"#,
                extraFiles: [
                    "ops/Brewfile": Self.helloBrewfile,
                    "dev/Brewfile": "brew \"jq\"\n",
                ]
            )

            let result = try await Self.wendyRun(cli, agent: agent, project: project)
            #expect(result.status.isFailure)
            #expect(result.stderr.contains("conflicts with another synced file"))
            #expect(!result.stdout.contains("Will apply Brewfile"))
            #expect(!result.stdout.contains("Brewfile applied."))
            #expect(!result.stdout.contains("SHOULD_NOT_START_AFTER_CONFLICT"))
        }
    }

    // MARK: - Helpers

    private static let helloBrewfile = "brew \"hello\"\n"

    private static func helloFormulaScenario() -> CLIAndAgentScenario {
        CLIAndAgentScenario(
            before: { _, agent in try await Self.uninstallHello(agent) },
            after: { _, agent in try await Self.uninstallHello(agent) }
        )
    }

    private static func brewRequiredScenario() -> CLIAndAgentScenario {
        CLIAndAgentScenario(before: { _, agent in try await Self.requireBrew(agent) })
    }

    private static func wendyRun(
        _ cli: WendyE2ESession,
        agent: WendyE2ESession,
        project: String
    ) async throws -> WendyE2EShellResult {
        try await cli.sh(
            "/usr/bin/env PATH=\"$PATH:/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin\" wendy run --yes --device \(Self.shQuote(agent.machine.address)) --prefix \(Self.shQuote(project)) --product BrewfileE2E"
        ) { result in
            result
        }
    }

    private static func makeSwiftPMProject(
        _ cli: WendyE2ESession,
        name: String,
        appID: String,
        appSource: String,
        brewfile: String? = nil,
        filesJSON: String = "[]",
        extraFiles: [String: String] = [:]
    ) async throws -> String {
        let project = try Self.makeProjectDirectory(cli, name: name)
        let brewfileLine = brewfile.map { ",\n  \"brewfile\": \"\($0)\"" } ?? ""
        try Self.writeFile(
            at: "\(project)/Package.swift",
            contents: """
                // swift-tools-version: 6.0
                import PackageDescription

                let package = Package(
                    name: "BrewfileE2E",
                    platforms: [.macOS(.v14)],
                    products: [.executable(name: "BrewfileE2E", targets: ["BrewfileE2E"])],
                    targets: [.executableTarget(name: "BrewfileE2E")]
                )
                """
        )
        try Self.writeFile(at: "\(project)/Sources/BrewfileE2E/main.swift", contents: appSource)
        try Self.writeFile(
            at: "\(project)/wendy.json",
            contents: """
                {
                  "appId": "\(appID)",
                  "version": "1.0.0",
                  "language": "swift",
                  "platform": "darwin",
                  "files": \(filesJSON)\(brewfileLine)
                }
                """
        )
        try Self.writeExtraFiles(project: project, files: extraFiles)
        return project
    }

    private static func makeXcodeProject(
        _ cli: WendyE2ESession,
        name: String,
        appID: String,
        extraFiles: [String: String]
    ) async throws -> String {
        let project = try Self.makeProjectDirectory(cli, name: name)
        try FileManager.default.createDirectory(
            atPath: "\(project)/BrewfileXcode.xcodeproj",
            withIntermediateDirectories: true
        )
        try Self.writeFile(
            at: "\(project)/wendy.json",
            contents: """
                {
                  "appId": "\(appID)",
                  "version": "1.0.0",
                  "language": "xcode",
                  "platform": "darwin",
                  "xcode": { "scheme": "BrewfileXcode" }
                }
                """
        )
        try Self.writeExtraFiles(project: project, files: extraFiles)
        return project
    }

    private static func makeProjectDirectory(_ cli: WendyE2ESession, name: String) throws -> String
    {
        guard let workingDirectory = cli.workingDirectory else {
            throw NSError(
                domain: "WendyRunWithNativeMacBrewfilesTests",
                code: 1,
                userInfo: [
                    NSLocalizedDescriptionKey: "CLI session must have a local working directory"
                ]
            )
        }
        let project = "\(workingDirectory)/\(name)"
        try? FileManager.default.removeItem(atPath: project)
        try FileManager.default.createDirectory(atPath: project, withIntermediateDirectories: true)
        return project
    }

    private static func writeExtraFiles(project: String, files: [String: String]) throws {
        for (relativePath, content) in files {
            try Self.writeFile(at: "\(project)/\(relativePath)", contents: content)
        }
    }

    private static func writeFile(at path: String, contents: String) throws {
        try FileManager.default.createDirectory(
            atPath: URL(fileURLWithPath: path).deletingLastPathComponent().path,
            withIntermediateDirectories: true
        )
        try contents.write(toFile: path, atomically: true, encoding: .utf8)
    }

    private static func installFakeXcodebuild(
        _ cli: WendyE2ESession,
        scheme: String
    ) async throws {
        guard scheme.range(of: #"^[A-Za-z0-9_-]+$"#, options: .regularExpression) != nil else {
            throw NSError(
                domain: "WendyRunWithNativeMacBrewfilesTests",
                code: 3,
                userInfo: [NSLocalizedDescriptionKey: "Xcode scheme is not shell-safe"]
            )
        }
        guard let binDirectory = cli.env["PATH"]?.split(separator: ":").first else {
            throw NSError(
                domain: "WendyRunWithNativeMacBrewfilesTests",
                code: 2,
                userInfo: [
                    NSLocalizedDescriptionKey: "CLI session PATH must include a bin directory"
                ]
            )
        }
        let scriptPath = "\(binDirectory)/xcodebuild"
        try Self.writeFile(
            at: scriptPath,
            contents: """
                #!/bin/sh
                set -eu
                for arg in "$@"; do
                  if [ "$arg" = "-list" ]; then
                    printf '{"project":{"schemes":["\(scheme)"]}}\n'
                    exit 0
                  fi
                done
                release_dir="$PWD/.xcode/Build/Products/Release"
                /bin/mkdir -p "$release_dir"
                product="$release_dir/\(scheme)"
                /bin/cat > "$product" <<'APP'
                #!/bin/sh
                set -eu
                for hello in /opt/homebrew/bin/hello /opt/homebrew/opt/hello/bin/hello /usr/local/bin/hello /usr/local/opt/hello/bin/hello; do
                  if [ -x "$hello" ]; then
                    printf 'XCODE_HELLO: %s\n' "$($hello)"
                    exit 0
                  fi
                done
                echo 'XCODE_HELLO_NOT_FOUND'
                exit 42
                APP
                /bin/chmod +x "$product"
                """
        )
        try FileManager.default.setAttributes(
            [.posixPermissions: 0o755],
            ofItemAtPath: scriptPath
        )
    }

    private static func requireBrew(_ agent: WendyE2ESession) async throws {
        try await agent.sh(posix: Self.brewShell("brew --version >/dev/null"), power: "throw")
    }

    private static func uninstallHello(_ agent: WendyE2ESession) async throws {
        try await agent.sh(
            posix: Self.brewShell(
                "brew uninstall --ignore-dependencies hello >/dev/null 2>&1 || true"
            ),
            power: "throw"
        )
    }

    private static func brewShell(_ body: String) -> String {
        """
        set -euo pipefail
        if [ -x /opt/homebrew/bin/brew ]; then
          brew=/opt/homebrew/bin/brew
        elif [ -x /usr/local/bin/brew ]; then
          brew=/usr/local/bin/brew
        else
          echo 'Homebrew is required for this E2E test' >&2
          exit 1
        fi
        \(body)
        """
    }

    private static func swiftMarkerAppSource(marker: String) -> String {
        """
        print("\(marker)")
        """
    }

    private static func swiftHelloAppSource(marker: String) -> String {
        """
        import Foundation

        let candidates = [
            "/opt/homebrew/bin/hello",
            "/opt/homebrew/opt/hello/bin/hello",
            "/usr/local/bin/hello",
            "/usr/local/opt/hello/bin/hello",
        ]
        guard let hello = candidates.first(where: { FileManager.default.isExecutableFile(atPath: $0) }) else {
            print("\(marker): HELLO_NOT_FOUND")
            exit(42)
        }

        let process = Process()
        process.executableURL = URL(fileURLWithPath: hello)
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        try process.run()
        process.waitUntilExit()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        let output = String(decoding: data, as: UTF8.self)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        print("\(marker): \\(output)")
        exit(process.terminationStatus)
        """
    }

    private static func shQuote(_ value: String) -> String {
        "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}
