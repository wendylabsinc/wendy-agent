// Prefer modular Foundation when present to minimize imports; fallback otherwise
#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// Ensures reported progress is monotonic non-decreasing.
/// Multiple subsystems may report jittery fractions; this actor clamps them.
// Expose to the whole package but not publicly outside it.
package actor Monotonic {
    private var last: Double = 0.0

    package init() {}

    /// Returns a value that is never less than any previously seen input.
    package func next(_ value: Double) -> Double {
        guard value.isFinite else { return last }
        if value > last { last = value }
        return last
    }
}
