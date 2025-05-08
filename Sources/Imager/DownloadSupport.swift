import Foundation
import AsyncHTTPClient
import _NIOFileSystem
import NIOCore

#if os(macOS)
    import Darwin
#elseif os(Linux)
    // No explicit libc import needed when using musl
#endif

// MARK: - Protocols

/// Protocol defining image downloading functionality
public protocol ImageDownloading {
    /// Downloads an image from a URL to a local temporary file and extracts the .img file if needed
    /// - Parameters:
    ///   - url: The URL to download from
    ///   - expectedSize: The expected size in bytes
    ///   - progressHandler: Closure to report download progress
    /// - Returns: The local file path where the image was saved
    func downloadImage(
        from url: URL,
        deviceName: String,
        expectedSize: Int,
        redownload: Bool,
        progressHandler: @escaping (Progress) -> Void
    ) async throws -> String
}

// MARK: - Implementations

/// Manages downloading device images from GCS
public class ImageDownloader: ImageDownloading {
    private let urlSession: URLSession
    private let fileManager: FileManager

    public init(urlSession: URLSession = .shared, fileManager: FileManager = .default) {
        self.urlSession = urlSession
        self.fileManager = fileManager
    }

    private func downloadFile(
        from url: URL,
        to path: String,
        progressHandler: @escaping (Progress) -> Void
    ) async throws {
        // Track how much data we've downloaded and when we last reported progress
        var bytesDownloaded: Int64 = 0

        // Get the total size of the download if needed
        let totalSize: Int64

        let request = HTTPClientRequest(url: url.absoluteString)
        let response = try await HTTPClient.shared.execute(request, deadline: NIODeadline.now() + .seconds(60))
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

    private func extractImage(
        from path: String,
        to directory: String, 
        progressHandler: @escaping (Progress) -> Void
    ) async throws -> String {
        // Report extraction progress
        let extractionProgress = Progress(totalUnitCount: 100)
        extractionProgress.completedUnitCount = 0
        progressHandler(extractionProgress)

        // Unzip the file using the unzip command line tool
        let unzipProcess = Process()

        // Check if unzip is available at the standard locations
        var unzipPath = "/usr/bin/unzip"
        if !fileManager.fileExists(atPath: unzipPath) {
            unzipPath = "/bin/unzip"
            if !fileManager.fileExists(atPath: unzipPath) {
                // Try to find unzip in PATH
                let whichUnzip = Process()
                whichUnzip.executableURL = URL(fileURLWithPath: "/bin/sh")
                whichUnzip.arguments = ["-c", "which unzip"]

                let outputPipe = Pipe()
                whichUnzip.standardOutput = outputPipe

                try whichUnzip.run()
                whichUnzip.waitUntilExit()

                if whichUnzip.terminationStatus == 0 {
                    let outputData = outputPipe.fileHandleForReading.readDataToEndOfFile()
                    if let path = String(data: outputData, encoding: .utf8)?.trimmingCharacters(
                        in: .whitespacesAndNewlines
                    ) {
                        unzipPath = path
                    }
                }
            }
        }

        if !fileManager.fileExists(atPath: unzipPath) {
            throw DownloadError.extractionFailed("Could not find 'unzip' utility on the system")
        }

        unzipProcess.executableURL = URL(fileURLWithPath: unzipPath)
        unzipProcess.arguments = [path, "-d", directory]

        // Set up pipes for output
        let outputPipe = Pipe()
        let errorPipe = Pipe()
        unzipProcess.standardOutput = outputPipe
        unzipProcess.standardError = errorPipe

        // Run the unzip process
        try unzipProcess.run()
        unzipProcess.waitUntilExit()

        // Check if unzip was successful
        if unzipProcess.terminationStatus != 0 {
            let errorData = errorPipe.fileHandleForReading.readDataToEndOfFile()
            let errorMessage = String(data: errorData, encoding: .utf8) ?? "Unknown error"
            throw DownloadError.extractionFailed(
                "Failed to extract ZIP file: \(errorMessage.trimmingCharacters(in: .whitespacesAndNewlines))"
            )
        }

        // Update extraction progress
        extractionProgress.completedUnitCount = 50
        progressHandler(extractionProgress)

        let imgPath = try await validateImage(at: directory)

        // Complete extraction progress
        extractionProgress.completedUnitCount = 100
        progressHandler(extractionProgress)

        // Remove the downloaded ZIP file to save space
        try? fileManager.removeItem(at: URL(fileURLWithPath: path))

        return imgPath
    }

    private func validateImage(at directory: String) async throws -> String {
        // Find the .img file in the extracted directory
        let enumerator = fileManager.enumerator(
            at: URL(fileURLWithPath: directory),
            includingPropertiesForKeys: nil,
            options: [.skipsHiddenFiles]
        )

        var imgFilePath: String?
        while let fileURL = enumerator?.nextObject() as? URL {
            if fileURL.pathExtension.lowercased() == "img" {
                imgFilePath = fileURL.path
                break
            }
        }

        guard let imgPath = imgFilePath else {
            throw DownloadError.extractionFailed("No .img file found in the downloaded archive")
        }

        return imgPath
    }

    public func downloadImage(
        from url: URL,
        deviceName: String,
        expectedSize: Int,
        redownload: Bool = false,
        progressHandler: @escaping (Progress) -> Void
    ) async throws -> String {
        let cacheDir = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(
            ".edge-cache/images"
        )
        let extractionDirectoryURL = cacheDir.appendingPathComponent(deviceName)
        let temporaryDirectory = fileManager.temporaryDirectory
        let tempFilename = UUID().uuidString
        let localZipURL = temporaryDirectory.appendingPathComponent("\(tempFilename).zip")

        func redownloadImage() async throws -> String {
            try await downloadFile(from: url, to: localZipURL.path, progressHandler: progressHandler)

            // Create the extraction directory
            try fileManager.createDirectory(
                at: extractionDirectoryURL,
                withIntermediateDirectories: true,
                attributes: nil
            )

            // Extract the .img file from the zip archive
            return try await extractImage(
                from: localZipURL.path, 
                to: extractionDirectoryURL.path, 
                progressHandler: progressHandler
            )
        }

        let isValidCache = try (!fileManager.fileExists(atPath: extractionDirectoryURL.path) ||
            FileManager.default.contentsOfDirectory(atPath: extractionDirectoryURL.path).isEmpty)

        if redownload || isValidCache {
            return try await redownloadImage()
        } else {
            print("Using cached image for \(deviceName)")

            do {
                return try await validateImage(at: extractionDirectoryURL.path)
            } catch {
                print("Invalid image found in cache, redownloading...")

                return try await redownloadImage()
            }
        }
    }
}

// MARK: - Factory

/// Factory for creating ImageDownloader instances
public enum ImageDownloaderFactory {
    /// Creates and returns a default ImageDownloader instance
    public static func createImageDownloader() -> ImageDownloading {
        return ImageDownloader()
    }
}

// MARK: - Errors

/// Errors related to download operations
public enum DownloadError: Error, LocalizedError {
    case fileCreationFailed
    case invalidResponse
    case downloadFailed(Error)
    case extractionFailed(String)

    public var errorDescription: String? {
        switch self {
        case .fileCreationFailed:
            return "Failed to create local file for download"
        case .invalidResponse:
            return "Invalid response from server"
        case .downloadFailed(let error):
            return "Download failed: \(error.localizedDescription)"
        case .extractionFailed(let reason):
            return "Failed to extract image file: \(reason)"
        }
    }
}
