import Testing
import WendyE2ETesting

@Suite
struct `'wendy cloud run'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy cloud run`. The output includes the command
     synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy cloud run --help") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Deprecated: use 'wendy run' instead"))
                #expect(result.stdout.contains("wendy cloud run [flags]"))
                #expect(result.stdout.contains("--build-type"))
                #expect(result.stdout.contains("--deploy"))
                #expect(result.stdout.contains("--detach"))
                #expect(result.stdout.contains("--user-args"))
                #expect(result.stdout.contains("--prefix"))
                #expect(result.stdout.contains("--device"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Reads the project configuration, builds the application image,
     deploys it through the Wendy Cloud tunnel broker, and starts the
     container. Success output makes the running app and target device
     clear.
     */
    @Test(
        .disabled(
            "WDY-1949/WDY-1950: cloud build/deploy/start needs isolated projects, builders, auth, tunnels, images, and a disposable managed-agent target."
        )
    )
    func `builds, deploys, and starts the current project`() async throws {}

    /**
     `--deploy` creates or updates the container on the target device and
     leaves it stopped. The command exits successfully after deployment and
     prints no live log stream.
     */
    @Test(
        .disabled(
            "WDY-1949/WDY-1950: stopped cloud deployment needs an isolated project and disposable managed-agent container state."
        )
    )
    func `deploys without starting when requested`() async throws {}

    /**
     `--detach` starts the application and returns after start-up status is
     known. Output includes the app name and how to view logs later.
     */
    @Test(
        .disabled(
            "WDY-1949/WDY-1950: detached cloud startup needs an isolated built app and disposable managed-agent target."
        )
    )
    func `detaches after starting when requested`() async throws {}

    /**
     `--user-args` preserves argument boundaries and forwards the provided
     values to the started application without interpreting secrets or shell
     metacharacters locally.
     */
    @Test(
        .disabled(
            "WDY-1949/WDY-1950: user-argument boundaries need an argv-reporting fixture app deployed through an isolated cloud tunnel."
        )
    )
    func `passes user arguments to the container`() async throws {}

    /**
     `--prefix` selects the project directory and `--device` names the cloud
     device and skips the picker. The command does not read unrelated
     `wendy.json` files or open interactive device selection.
     */
    @Test(
        .disabled(
            "WDY-1949/WDY-1950: explicit project/device success needs isolated project, auth, tunnel, and managed-target fixtures."
        )
    )
    func `uses explicit project and device selection`() async throws {}

    /**
     Requires a valid Wendy Cloud auth session before opening the tunnel.
     Missing or ambiguous sessions produce an auth diagnostic without
     building, deploying, or contacting a device.
     */
    @Test
    func `uses cloud authentication before connecting`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy cloud run --device target --json") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("not logged in"))
                #expect(result.stderr.contains("wendy auth login"))
            }
        }
    }

    /**
     Build failures, invalid project configuration, unreachable devices, or
     deployment errors return a failure status. Partial remote resources are
     either cleaned up or identified clearly for manual cleanup.
     */
    @Test(
        .disabled(
            "WDY-1949/WDY-1950: build/deployment failure cleanup needs controllable builder, tunnel, and managed-agent failure modes."
        )
    )
    func `reports build or deployment failure without claiming success`() async throws {}

    /**
     With `--json`, emits structured build, deploy, start, and app metadata.
     Progress and streamed container logs do not corrupt stdout JSON.
     */
    @Test(
        .disabled(
            "WDY-1909/WDY-1949/WDY-1950: cloud run lacks complete JSON results and needs isolated deployment fixtures to verify stream separation."
        )
    )
    func `prints JSON run metadata for automation`() async throws {}

    /**
     Rejects a project prefix that does not resolve to an existing directory.

     Local path validation fails before cloud authentication, build, deployment,
     or tunnel setup.
     */
    @Test
    func `rejects a missing project prefix before cloud access`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy cloud run --prefix missing-project") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("resolving working directory"))
                #expect(result.stderr.contains("does not exist"))
                #expect(!result.stderr.contains("not logged in"))
            }
        }
    }

    /**
     Rejects flags that are not part of the command's documented interface.

     The command reports a usage error on stderr and does not perform the
     requested operation.
     */
    @Test
    func `rejects undocumented flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy cloud run --bogus") { result in
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
            "WDY-1934: 'wendy cloud run' silently accepts positional arguments because the command has no cobra.NoArgs validator."
        )
    )
    func `rejects undocumented positional arguments`() async throws {}
}
