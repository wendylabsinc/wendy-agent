import Testing
import WendyE2ETesting

@Suite
struct `'wendy os download'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy os download`. The output includes the command
     synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy os download --help") { result in
                let stdout = result.stdout
                #expect(result.status.isSuccess)
                #expect(stdout.contains("Download a WendyOS image"))
                #expect(stdout.contains("wendy os download [flags]"))
                #expect(stdout.contains("--version"))
                #expect(stdout.contains("--overwrite"))
                #expect(stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Downloads the requested WendyOS image version, verifies the artifact,
     and stores it in the OS cache for later installation. Success
     output includes device type, version, cache path, and size.
     */
    @Test(
        .disabled(
            "WDY-1944: deterministic download success requires a pinned local manifest/artifact server and checksum fixture rather than the mutable public catalog."
        )
    )
    func `downloads a selected WendyOS image into the cache`() async throws {
        // TODO: enable with the protected OS artifact fixture (WDY-1944).
    }

    /**
     When the requested image already exists and verifies successfully,
     uses the cached artifact. `--overwrite` replaces it after a
     successful new download.
     */
    @Test(
        .disabled(
            "WDY-1944: cache-hit and overwrite coverage requires a pinned manifest plus known artifact/checksum bytes."
        )
    )
    func `uses cached images unless overwrite is requested`() async throws {
        // TODO: enable with the protected OS artifact fixture (WDY-1944).
    }

    /**
     Unknown versions, unavailable manifests, network failures, or failed
     verification leave existing cached artifacts untouched and report a
     failure on stderr.
     */
    @Test(
        .disabled(
            "WDY-1944: unavailable-version, network-failure, and checksum-failure paths need a controllable local manifest/artifact server."
        )
    )
    func `reports unavailable versions without changing the cache`() async throws {
        // TODO: enable with failure modes from the protected OS artifact fixture (WDY-1944).
    }

    /**
     With `--json`, emits one JSON object containing version, device type,
     artifact path, checksum, byte count, and cache-hit status.
     */
    @Test(
        .disabled(
            "WDY-1909: 'wendy os download' does not implement global --json; WDY-1944 tracks the deterministic artifact fixture needed to exercise a successful result."
        )
    )
    func `prints JSON download metadata for automation`() async throws {
        // TODO: enable when download implements JSON and has a pinned fixture (WDY-1909, WDY-1944).
    }

    /**
     Rejects flags that are not part of the command's documented interface.

     The command reports a usage error on stderr and does not perform the
     requested operation.
     */
    @Test
    func `rejects undocumented flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy os download --bogus") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("unknown flag"))
                #expect(result.stderr.contains("--bogus"))
            }
        }
    }

    /**
     Rejects positional arguments because this command is entirely flag-driven.

     The command reports a usage error instead of treating undocumented input as
     a valid request.
     */
    @Test(
        .disabled(
            "WDY-1934: 'wendy os download' silently accepts extra positional arguments because the leaf command has no cobra.NoArgs validator."
        )
    )
    func `rejects undocumented positional arguments`() async throws {
        // TODO: enable when OS download rejects positional arguments (WDY-1934).
    }
}
