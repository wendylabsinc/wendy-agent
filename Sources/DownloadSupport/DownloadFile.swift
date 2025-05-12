import AsyncHTTPClient
import Foundation
import NIOCore
import NIOFoundationCompat
import _NIOFileSystem

enum DownloadError: Error {
    case invalidResponse
}

public func downloadFile(
    from url: URL,
    to path: String,
    progressHandler: @escaping (Progress) -> Void
) async throws {
    // Track how much data we've downloaded and when we last reported progress
    var bytesDownloaded: Int64 = 0

    // Get the total size of the download if needed
    let totalSize: Int64

    let request = HTTPClientRequest(url: url.absoluteString)
    let response = try await HTTPClient.shared.execute(
        request,
        deadline: NIODeadline.now() + .seconds(60)
    )
    guard
        let contentLength = response.headers.first(name: "Content-Length").flatMap(Int64.init),
        contentLength > 0
    else {
        throw DownloadError.invalidResponse
    }

    totalSize = contentLength
    let progress = Progress(totalUnitCount: totalSize)

    try await FileSystem.shared.withFileHandle(
        forWritingAt: FilePath(path),
        options: .newFile(replaceExisting: true)
    ) { handle in
        var writer = handle.bufferedWriter(startingAtAbsoluteOffset: 0)
        try await writer.flush()
        for try await chunk in response.body {
            let bytesWritten = try await writer.write(contentsOf: chunk)
            bytesDownloaded += Int64(bytesWritten)
            progress.completedUnitCount = bytesDownloaded
            progressHandler(progress)
        }

        try await writer.flush()
    }

    // Ensure final progress is reported
    progress.completedUnitCount = totalSize
    progressHandler(progress)
}
