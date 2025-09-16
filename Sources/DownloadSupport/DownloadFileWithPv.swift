import Foundation
import Subprocess

public enum PvDownloadError: Error {
    case pvNotInstalled
    case downloadFailed(String)
}

/// Download a file using curl and pv for progress display
public func downloadFileWithPv(
    from url: URL,
    to path: String,
    expectedSize: Int64? = nil
) async throws {
    // Check if pv is available
    let pvCheckResult = try await Subprocess.run(
        Subprocess.Executable.name("which"),
        arguments: ["pv"],
        output: .string(limit: .max),
        error: .discarded
    )

    guard pvCheckResult.terminationStatus.isSuccess else {
        throw PvDownloadError.pvNotInstalled
    }

    // Create the download command
    // curl -L: Follow redirects
    // curl -f: Fail on HTTP errors
    // curl -s: Silent mode (no progress from curl)
    // pv -f: Force output even if not to terminal
    // pv -p: Show progress bar
    // pv -e: Show ETA
    // pv -r: Show rate
    // pv -b: Show bytes transferred
    var script: String

    if let expectedSize = expectedSize, expectedSize > 0 {
        // If we know the size, use it for percentage calculation
        script = """
            curl -L -f -s "\(url.absoluteString)" | pv -fperb -s \(expectedSize) > "\(path)"
            """
    } else {
        // Without size, just show bytes and rate
        script = """
            curl -L -f -s "\(url.absoluteString)" | pv -ftrb > "\(path)"
            """
    }

    // Run the download
    let result = try await Subprocess.run(
        Subprocess.Executable.name("bash"),
        arguments: ["-c", script],
        output: .discarded,
        // Let pv output to terminal
        error: .fileDescriptor(.standardError, closeAfterSpawningProcess: false)
    )

    if !result.terminationStatus.isSuccess {
        // Clean up partial download
        try? FileManager.default.removeItem(atPath: path)
        throw PvDownloadError.downloadFailed(
            "Download failed with status: \(result.terminationStatus)"
        )
    }
}

/// Download a file with fallback to AsyncHTTPClient if pv is not available
public func downloadFileWithProgress(
    from url: URL,
    to path: String,
    expectedSize: Int64? = nil,
    progressHandler: @escaping (Progress) -> Void
) async throws {
    // First try with pv
    do {
        try await downloadFileWithPv(from: url, to: path, expectedSize: expectedSize)

        // Send 100% completion if we have expected size
        if let expectedSize = expectedSize {
            let progress = Progress(totalUnitCount: expectedSize)
            progress.completedUnitCount = expectedSize
            progressHandler(progress)
        }
    } catch PvDownloadError.pvNotInstalled {
        // Fallback to original download method
        // Import the downloadFile function from the same module
        try await downloadFile(from: url, to: path, progressHandler: progressHandler)
    }
}
