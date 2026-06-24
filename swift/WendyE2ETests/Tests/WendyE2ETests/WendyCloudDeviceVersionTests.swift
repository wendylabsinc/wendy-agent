import Testing

/// Hidden deprecated compatibility command for `wendy cloud device info`.
///
/// Use `wendy cloud device info` in new scripts and documentation.
@Suite
struct `'wendy cloud device version'` {
    // MARK: - Compatibility

    /**
     The hidden command remains directly invocable for older scripts, but
     `wendy cloud device --help` does not advertise it. Direct help preserves
     the `wendy cloud device info` option surface for users who still discover
     the legacy command explicitly.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `is hidden from parent help while direct help mirrors '... cloud device info'`()
        async throws
    {
        // TODO: implement.
    }

    /**
     In human-readable mode, the deprecated command reports the same cloud-routed
     device information as `wendy cloud device info` and writes a deprecation
     warning that names `wendy cloud device info` as the replacement command.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `aliases '... cloud device info' with a deprecation notice`() async throws {
        // TODO: implement.
    }

    /**
     With `--json` or non-interactive JSON output, deprecation guidance stays
     out of stdout and stderr so existing scripts can continue parsing the
     response.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `'--json' keeps JSON output clean`() async throws {
        // TODO: implement.
    }
}
