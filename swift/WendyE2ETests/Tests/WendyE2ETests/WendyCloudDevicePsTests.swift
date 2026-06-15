import Testing

/// Public compatibility alias for `wendy cloud device apps list`.
///
/// The short `ps` form remains visible in cloud-routed device help for users who
/// expect a container-style listing command.
@Suite
struct `'wendy cloud device ps'` {
    // MARK: - Compatibility

    /**
     Displays usage for `wendy cloud device ps`. The output identifies the
     command as an alias for `wendy cloud device apps list`, lists cloud and
     global inherited flags, exits successfully, and emits no stderr.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `prints '... cloud device ps' alias help`() async throws {
        // TODO: implement.
    }

    /**
     Produces the same cloud-routed application inventory as `wendy cloud device
     apps list` after selecting and authenticating the cloud device. The alias
     does not introduce additional prompts or state changes.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `aliases '... cloud device apps list'`() async throws {
        // TODO: implement.
    }

    /**
     With `--json`, emits the same application inventory schema as `wendy cloud
     device apps list` and keeps stdout machine-readable for automation.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `'--json' keeps '... cloud device apps list' output clean`() async throws {
        // TODO: implement.
    }
}
