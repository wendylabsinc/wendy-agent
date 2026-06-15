import Testing

/// Public compatibility alias for `wendy device apps list`.
///
/// The short `ps` form remains visible in help for users who expect a
/// container-style listing command.
@Suite
struct `'wendy device ps'` {
    // MARK: - Compatibility

    /**
     Displays usage for `wendy device ps`. The output identifies the command as
     an alias for `wendy device apps list`, lists the same inherited global
     flags, exits successfully, and emits no stderr.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `prints '... device ps' alias help`() async throws {
        // TODO: implement.
    }

    /**
     Produces the same human-readable application inventory as `wendy device
     apps list`, including empty-device output and table formatting. The alias
     does not introduce additional prompts or state changes.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `aliases '... device apps list'`() async throws {
        // TODO: implement.
    }

    /**
     With `--json`, emits the same application inventory schema as `wendy device
     apps list` and keeps stdout machine-readable for automation.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `'--json' keeps '... device apps list' output clean`() async throws {
        // TODO: implement.
    }
}
