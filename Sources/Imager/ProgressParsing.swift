// Prefer the new modular Foundation when available, fallback otherwise
#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// Parse a dd progress line and extract the cumulative bytes value.
/// Supports both:
/// - Linux:  "123456 bytes (123 MB, ...) copied, ..."
/// - macOS:  "123456 bytes transferred in ..."
/// Returns nil if no byte count is found.
internal func parseBytesTransferred(from text: String) -> Int64? {
    // Use Swift Regex for portability and performance
    let pattern = /(\d+)\s+bytes\b/
    if let m = text.firstMatch(of: pattern) {
        return Int64(String(m.1))
    }
    return nil
}
