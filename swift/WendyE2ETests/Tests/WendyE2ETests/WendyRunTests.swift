import Testing
import WendyE2ETesting

@Suite
struct `'wendy run'` {
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
    @Test(
        .enabled(if: isAgentLinuxOrWendyOS),
        .disabled("SPEC STUB: behavior agreed, implementation pending")
    )
    func `syncs configured files into a Linux container`() async throws {
        // TODO: implement.
    }

    /**
     Re-running a project with changed `wendy.json.files` updates the
     app-scoped file-sync area before the replacement container starts. Updated
     files are visible with their new contents and mode, removed declarations
     are pruned from the managed app file-sync area, and unrelated app data such
     as persistent volumes remains untouched.
     */
    @Test(
        .enabled(if: isAgentLinuxOrWendyOS),
        .disabled("SPEC STUB: behavior agreed, implementation pending")
    )
    func `updates synced files and prunes stale paths on redeploy`() async throws {
        // TODO: implement.
    }

    /**
     Files and directories declared in top-level `wendy.json.files` are mounted
     read-only into Linux containers. An app that attempts to overwrite or
     remove a declared file receives a filesystem failure, and neither the
     original project file nor the agent-managed synced copy is mutated by the
     running container.
     */
    @Test(
        .enabled(if: isAgentLinuxOrWendyOS),
        .disabled("SPEC STUB: behavior agreed, implementation pending")
    )
    func `mounts synced files read-only in the container`() async throws {
        // TODO: implement.
    }

    /**
     Unsafe file-sync paths are rejected before deployment. Absolute `path` or
     `to` values, empty destinations, and values containing `..` produce an
     actionable configuration diagnostic, return a failure status, and do not
     build an image, create a container, or write outside the app-scoped
     file-sync area.
     */
    @Test(
        .enabled(if: isAgentLinuxOrWendyOS),
        .disabled("SPEC STUB: behavior agreed, implementation pending")
    )
    func `rejects unsafe configured file paths before deployment`() async throws {
        // TODO: implement.
    }

    /**
     Multi-service and Compose deployments do not consume top-level
     `wendy.json.files` yet. A project that combines those deployment modes
     with top-level file declarations reports the unsupported combination
     clearly instead of silently ignoring files or mounting them into only one
     service.
     */
    @Test(
        .enabled(if: isAgentLinuxOrWendyOS),
        .disabled("SPEC STUB: behavior agreed, implementation pending")
    )
    func `reports top-level files as unsupported for multi-service deployments`() async throws {
        // TODO: implement.
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
}
