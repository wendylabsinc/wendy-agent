import DownloadSupport
import Foundation
import Logging
import Shell

#if canImport(FoundationNetworking)
    import FoundationNetworking
#endif

struct Release: Decodable {
    struct Asset: Decodable {
        let browser_download_url: String
        let name: String
        let content_type: String
    }
    let prerelease: Bool
    let assets: [Asset]
    let name: String
}

enum ReleasesError: Error {
    case invalidResponse
    case noReleases
    case noAsset
}

func downloadLatestRelease() async throws -> URL {
    let releases = try await fetchReleases()
    guard let latestRelease = releases.first else {
        throw ReleasesError.noReleases
    }
    guard
        let asset = latestRelease.assets.first(where: {
            $0.name.contains("edge-agent-linux-static-musl-aarch64")
        })
    else {
        throw ReleasesError.noAsset
    }
    let downloadedFileURL = try await downloadAsset(asset)
    let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    let fileURL = try await extract(at: downloadedFileURL, to: directory) { file in
        file.lastPathComponent == "edge-agent"
    }
    try? FileManager.default.removeItem(at: downloadedFileURL)
    return fileURL
}

func fetchReleases() async throws -> [Release] {
    let githubReleasesURL = URL(
        string: "https://api.github.com/repos/edgeengineer/edge-agent/releases"
    )!

    // Fetch releases JSON
    let logger = Logger(label: "edge.utils.fetchReleases")
    logger.info("Fetching all releases...")
    let (data, response) = try await URLSession.shared.data(from: githubReleasesURL)
    guard let httpResponse = response as? HTTPURLResponse, httpResponse.statusCode == 200 else {
        logger.error("Failed to fetch releases: HTTP error")
        throw ReleasesError.invalidResponse
    }

    return try JSONDecoder().decode([Release].self, from: data)
}

func downloadAsset(_ asset: Release.Asset) async throws -> URL {
    let fileManager = FileManager.default
    let tempDir = fileManager.temporaryDirectory
        .appendingPathComponent(UUID().uuidString)
    try fileManager.createDirectory(at: tempDir, withIntermediateDirectories: true)

    let downloadURL = URL(string: asset.browser_download_url)!
    let downloadedFileURL = tempDir.appendingPathComponent(asset.name)
    try await downloadFile(from: downloadURL, to: downloadedFileURL.path) { _ in }
    print("Downloaded edge-agent: \(downloadedFileURL.path)")
    return downloadedFileURL
}

enum ExtractError: Error {
    case failedToExtract
    case executableNotFound
}

func extract(
    at url: URL,
    to tempDir: URL,
    findExecutable: (URL) -> Bool
) async throws -> URL {
    // Determine if it's a tar.gz or a binary
    let isTarGz = url.pathExtension == "gz" || url.lastPathComponent.contains("tar.gz")
    guard isTarGz else {
        print("File is not a tar.gz file")
        return url
    }

    print("Extracting tar.gz file...")
    let extractDir = tempDir.appendingPathComponent("extract")
    try FileManager.default.createDirectory(at: extractDir, withIntermediateDirectories: true)
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
    process.arguments = ["tar", "-xzf", url.path, "-C", extractDir.path]
    try process.run()
    process.waitUntilExit()
    guard process.terminationStatus == 0 else {
        print("Failed to extract tar.gz archive")
        throw ExtractError.failedToExtract
    }

    // Find the binary in the extracted directory
    let enumerator = FileManager.default.enumerator(at: extractDir, includingPropertiesForKeys: nil)
    while let file = enumerator?.nextObject() as? URL {
        if findExecutable(file) {
            return file
        }
    }
    throw ExtractError.executableNotFound
}
