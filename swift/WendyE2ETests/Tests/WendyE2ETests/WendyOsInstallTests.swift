import Testing
import WendyE2ETesting

@Suite
struct `'wendy os install'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy os install`. The output includes the command
     synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy os install --help") { result in
                let stdout = result.stdout
                #expect(result.status.isSuccess)
                #expect(stdout.contains("Interactively select a supported device"))
                #expect(stdout.contains("wendy os install [image] [drive] [flags]"))
                #expect(stdout.contains("--force"))
                #expect(stdout.contains("--yes-overwrite-internal"))
                #expect(stdout.contains("--wifi"))
                #expect(stdout.contains("--pre-enroll"))
                #expect(stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Writes the selected WendyOS image or firmware to the target drive
     after the user confirms the destructive operation. Output reports
     progress and final device preparation status.
     */
    @Test(
        .disabled(
            "WDY-1944: install success requires a disposable virtual block device and pinned image fixture with safeguards against host disks."
        )
    )
    func `installs an image to a selected removable drive`() async throws {
        // TODO: enable with the protected virtual-drive fixture (WDY-1944).
    }

    /**
     When image path, drive id, and `--force` are provided, skips
     interactive pickers and confirmation prompts while preserving the
     same safety checks for drive identity.
     */
    @Test(
        .disabled(
            "WDY-1944: non-interactive flashing still requires a disposable virtual drive; physical runner disks must remain unavailable to E2E."
        )
    )
    func `runs non-interactively with image, drive, and force`() async throws {
        // TODO: enable with the protected virtual-drive fixture (WDY-1944).
    }

    /**
     Internal or non-removable drives are protected. Non-interactive
     installs require the dedicated overwrite flag before any bytes are
     written.
     */
    @Test(
        .disabled(
            "WDY-1944: internal-drive safety needs a synthetic inventory and disposable target; it cannot be asserted against a runner's real disks."
        )
    )
    func `refuses to overwrite internal drives without explicit consent`() async throws {
        // TODO: enable with the protected virtual-drive inventory (WDY-1944).
    }

    /**
     WiFi flags and device-name flags are written into first-boot
     configuration for the image. Invalid WiFi definitions fail before
     the target drive is modified.
     */
    @Test(
        .disabled(
            "WDY-1944: first-boot preseed verification requires a pinned image that can be mounted and inspected without touching a physical drive."
        )
    )
    func `preseeds WiFi and device identity when requested`() async throws {
        // TODO: enable with the protected image and virtual-drive fixture (WDY-1944).
    }

    /**
     `--pre-enroll` uses the stored auth session to add enrollment data
     to the image. Missing or expired auth fails before writing the
     drive.
     */
    @Test(
        .disabled(
            "WDY-1944: pre-enrollment needs protected auth, a pinned image, and a disposable drive fixture; personal auth and physical disks are prohibited."
        )
    )
    func `pre-enrolls only with valid cloud authentication`() async throws {
        // TODO: enable with protected cloud and OS install fixtures (WDY-1944).
    }

    /**
     With `--json`, emits structured drive, image, version, and outcome
     metadata. Progress output does not corrupt stdout JSON.
     */
    @Test(
        .disabled(
            "WDY-1909: 'wendy os install' does not implement global --json; WDY-1944 tracks the protected fixture needed for a successful install."
        )
    )
    func `prints JSON install result for automation`() async throws {
        // TODO: enable when install implements JSON and has protected fixtures (WDY-1909, WDY-1944).
    }

    /**
     Rejects incomplete positional mode and mutually exclusive direct/manifest
     options before image, drive, elevation, network, or auth access.
     */
    @Test
    func `validates install mode arguments before destructive work`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy os install image-only") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("must be provided as [image] [drive]"))
            }
            try await cli.sh("wendy os install image drive extra") { result in
                #expect(result.status.isFailure)
                #expect(result.stderr.contains("accepts at most 2 arg"))
            }
            try await cli.sh("wendy os install image drive --rootfs-only") { result in
                #expect(result.status.isFailure)
                #expect(result.stderr.contains("--rootfs-only cannot be combined"))
            }
            try await cli.sh("wendy os install image drive --device-type raspberry-pi-5") {
                result in
                #expect(result.status.isFailure)
                #expect(
                    result.stderr.contains(
                        "positional [image] [drive] arguments cannot be combined"
                    )
                )
            }
            try await cli.sh("wendy os install --nightly --version 1.0.0") { result in
                #expect(result.status.isFailure)
                #expect(result.stderr.contains("--nightly and --version are mutually exclusive"))
            }
        }
    }

    /**
     Rejects flags that are not part of the command's documented interface.

     The command reports a usage error on stderr and does not perform the
     requested operation.
     */
    @Test
    func `rejects unknown flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy os install --bogus") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("unknown flag"))
                #expect(result.stderr.contains("--bogus"))
            }
        }
    }
}
