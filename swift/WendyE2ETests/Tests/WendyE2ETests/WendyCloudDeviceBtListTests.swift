import Testing

/// Public alias path for `wendy cloud device bluetooth list`.
///
/// The `bt` shorthand remains available for cloud-routed command entry and shell
/// completion, while canonical documentation continues to prefer `bluetooth`.
@Suite
struct `'wendy cloud device bt list'` {
    // MARK: - Compatibility

    /**
     Resolves through the `bt` alias and displays the same help as `wendy cloud
     device bluetooth list`, including inherited cloud/device/global flags and
     validation behavior.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `prints '... bluetooth list help' through the 'bt' alias`() async throws {
        // TODO: implement.
    }

    /**
     Lists Bluetooth peripherals over the cloud tunnel using the same output
     contract as `wendy cloud device bluetooth list`. The alias does not change
     cloud authentication, target selection, JSON output, or error diagnostics.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `aliases '... cloud device bluetooth list'`() async throws {
        // TODO: implement.
    }
}
