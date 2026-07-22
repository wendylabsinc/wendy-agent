import Testing
import WendyE2ETesting

@Suite
struct `'wendy os update'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy os update`. The output includes the command
     synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy os update --help") { result in
                let stdout = result.stdout
                #expect(result.status.isSuccess)
                #expect(stdout.contains("Update WendyOS using an OS update artifact"))
                #expect(stdout.contains("wendy os update [artifact-path] [flags]"))
                #expect(stdout.contains("--artifact-url"))
                #expect(stdout.contains("--nightly"))
                #expect(stdout.contains("--pr"))
                #expect(stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Serves the provided Mender artifact to the selected device and
     requests an OS update. Success output identifies the artifact and
     device update status.
     */
    @Test(
        .disabled(
            "WDY-1944: local-artifact OTA success requires a hosted update-capable WendyOS fixture and pinned Mender artifact."
        )
    )
    func `updates WendyOS from a local artifact`() async throws {
        // TODO: enable with the protected hosted OTA fixture (WDY-1944).
    }

    /**
     `--artifact-url` instructs the device to fetch a remote artifact
     directly. The URL is validated before the update request is sent.
     */
    @Test(
        .disabled(
            "WDY-1945: --artifact-url is not validated until after device connection and version RPCs, so malformed URLs cannot fail locally as specified."
        )
    )
    func `updates WendyOS from a remote artifact URL`() async throws {
        // TODO: enable when URL validation precedes device access (WDY-1945).
    }

    /**
     `--nightly` selects the latest prerelease OS and agent artifacts
     from the manifest and reports the chosen versions before updating.
     */
    @Test(
        .disabled(
            "WDY-1944: nightly resolution and update success need a pinned manifest plus hosted update-capable WendyOS fixture."
        )
    )
    func `uses nightly artifacts when requested`() async throws {
        // TODO: enable with protected manifest and OTA fixtures (WDY-1944).
    }

    /**
     If the target device cannot be reached, the command stops any
     temporary local artifact server and exits with a clear diagnostic.
     */
    @Test(
        .disabled(
            "WDY-1944: cleanup observation needs a controllable hosted OTA target and local artifact-server fixture rather than a physical device."
        )
    )
    func `reports unreachable devices without serving stale artifacts`() async throws {
        // TODO: enable with protected OTA and artifact-server fixtures (WDY-1944).
    }

    /**
     With `--json`, emits one JSON object containing device, artifact,
     version, and update request status fields.
     */
    @Test(
        .disabled(
            "WDY-1909: 'wendy os update' does not implement global --json; WDY-1944 tracks the hosted fixture needed for update metadata."
        )
    )
    func `prints JSON update metadata for automation`() async throws {
        // TODO: enable when update implements JSON and has a hosted fixture (WDY-1909, WDY-1944).
    }

    /**
     Rejects requests that specify more than one operating-system artifact source.

     Source validation fails before downloading, contacting update services, or
     changing a device.
     */
    @Test
    func `rejects conflicting artifact sources locally`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                "wendy os update local.mender --artifact-url https://example.invalid/update.mender"
            ) { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("either a local artifact path or --artifact-url"))
            }
            try await cli.sh("wendy os update local.mender --pr 123") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("--pr cannot be combined"))
            }
        }
    }

    /**
     Accepts only the documented arguments and flags for `wendy os update`.
     Extra positional arguments or unknown flags produce a usage diagnostic
     on stderr, return a failure status, emit no success output, and leave
     existing state unchanged.
     */
    @Test
    func `rejects undocumented arguments and flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy os update first.mender second.mender") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("accepts at most 1 arg"))
            }
            try await cli.sh("wendy os update --bogus") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("unknown flag"))
                #expect(result.stderr.contains("--bogus"))
            }
        }
    }
}
