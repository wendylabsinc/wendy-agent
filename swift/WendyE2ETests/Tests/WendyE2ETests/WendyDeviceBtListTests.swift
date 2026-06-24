import Testing

/// Public alias path for `wendy device bluetooth list`.
///
/// The `bt` shorthand remains available for command entry and shell completion,
/// while canonical documentation continues to prefer `bluetooth`.
@Suite
struct `'wendy device bt list'` {
    // MARK: - Compatibility

    /**
     Resolves through the `bt` alias and displays the same help as `wendy device
     bluetooth list`, including inherited device/global flags and validation
     behavior.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `prints '... bluetooth list help' through the 'bt' alias`() async throws {
        // TODO: implement.
    }

    /**
     Lists Bluetooth peripherals using the same output contract as `wendy device
     bluetooth list`. The alias does not change target selection, JSON output,
     or error diagnostics.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `aliases '... device bluetooth list'`() async throws {
        // TODO: implement.
    }
}
