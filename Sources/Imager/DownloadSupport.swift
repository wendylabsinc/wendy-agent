import Foundation
#if os(macOS)
import Darwin
#elseif os(Linux)
import Glibc
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
    func downloadImage(from url: URL, expectedSize: Int, progressHandler: @escaping (Progress) -> Void) async throws -> String
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
    
    public func downloadImage(from url: URL, expectedSize: Int, progressHandler: @escaping (Progress) -> Void) async throws -> String {
        let temporaryDirectory = fileManager.temporaryDirectory
        let tempFilename = UUID().uuidString
        let localZipURL = temporaryDirectory.appendingPathComponent("\(tempFilename).zip")
        
        // If the expected size is known, create a progress object
        let progress = Progress(totalUnitCount: Int64(expectedSize))
        
        // Create a temporary file to write to
        guard fileManager.createFile(atPath: localZipURL.path, contents: nil) else {
            throw DownloadError.fileCreationFailed
        }
        
        // Track how much data we've downloaded and when we last reported progress
        var bytesDownloaded: Int64 = 0
        var lastProgressUpdate = Date()
        
        // Use a larger chunk size for efficient retrieval
        let chunkSize = 1024 * 1024 // 1MB chunks
        
        // Get the total size of the download if needed
        var totalSize = Int64(expectedSize)
        if totalSize <= 0 {
            let (_, response) = try await urlSession.data(for: URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 5))
            if let httpResponse = response as? HTTPURLResponse, 
               let contentLength = httpResponse.value(forHTTPHeaderField: "Content-Length"),
               let size = Int64(contentLength) {
                totalSize = size
                progress.totalUnitCount = size
            }
        }
        
        // Create a URL request with a range header
        var request = URLRequest(url: url)
        
        // Create a file handle for writing
        guard let fileHandle = try? FileHandle(forWritingTo: localZipURL) else {
            throw DownloadError.fileCreationFailed
        }
        
        defer {
            try? fileHandle.close()
        }
        
        // Download in chunks to report progress
        while bytesDownloaded < totalSize {
            // Set range header for the next chunk
            let rangeStart = bytesDownloaded
            let rangeEnd = min(rangeStart + Int64(chunkSize) - 1, totalSize - 1)
            request.setValue("bytes=\(rangeStart)-\(rangeEnd)", forHTTPHeaderField: "Range")
            
            // Download the chunk
            let (data, response) = try await urlSession.data(for: request)
            
            guard let httpResponse = response as? HTTPURLResponse,
                  httpResponse.statusCode == 206 || httpResponse.statusCode == 200 else {
                throw DownloadError.invalidResponse
            }
            
            // Write the chunk to file
            try fileHandle.write(contentsOf: data)
            
            // Update download progress
            bytesDownloaded += Int64(data.count)
            progress.completedUnitCount = bytesDownloaded
            
            // Only report progress at most once per second to avoid UI thrashing
            let now = Date()
            if now.timeIntervalSince(lastProgressUpdate) >= 1.0 {
                lastProgressUpdate = now
                progressHandler(progress)
            }
        }
        
        // Ensure final progress is reported
        progress.completedUnitCount = totalSize
        progressHandler(progress)
        
        // Extract the .img file from the zip archive
        let extractionDirectoryURL = temporaryDirectory.appendingPathComponent("\(tempFilename)-extracted")
        
        // Create the extraction directory
        try fileManager.createDirectory(at: extractionDirectoryURL, withIntermediateDirectories: true, attributes: nil)
        
        // Report extraction progress
        let extractionProgress = Progress(totalUnitCount: 100)
        extractionProgress.completedUnitCount = 0
        progressHandler(extractionProgress)
        
        // Unzip the file using the unzip command line tool
        #if os(macOS)
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/bin/unzip")
        task.arguments = [localZipURL.path, "-d", extractionDirectoryURL.path]
        try task.run()
        task.waitUntilExit()
        
        // Check if the unzipping was successful
        if task.terminationStatus != 0 {
            throw DownloadError.extractionFailed("Failed to extract ZIP file, exit code: \(task.terminationStatus)")
        }
        #elseif os(Linux)
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/bin/unzip")
        task.arguments = [localZipURL.path, "-d", extractionDirectoryURL.path]
        try task.run()
        task.waitUntilExit()
        
        // Check if the unzipping was successful
        if task.terminationStatus != 0 {
            throw DownloadError.extractionFailed("Failed to extract ZIP file, exit code: \(task.terminationStatus)")
        }
        #endif
        
        // Update extraction progress
        extractionProgress.completedUnitCount = 50
        progressHandler(extractionProgress)
        
        // Find the .img file in the extracted directory
        let enumerator = fileManager.enumerator(at: extractionDirectoryURL, 
                                                includingPropertiesForKeys: nil,
                                                options: [.skipsHiddenFiles])
        
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
        
        // Complete extraction progress
        extractionProgress.completedUnitCount = 100
        progressHandler(extractionProgress)
        
        // Remove the downloaded ZIP file to save space
        try? fileManager.removeItem(at: localZipURL)
        
        return imgPath
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