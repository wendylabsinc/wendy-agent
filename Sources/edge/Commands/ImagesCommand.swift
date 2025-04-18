import ArgumentParser
import Foundation
import Logging

// MARK: - Models

/// Top-level response for listing objects in a GCS bucket.
struct GCSObjectList: Decodable {
    struct Item: Decodable {
        let name: String
        let size: String?
        let contentType: String?
    }
    let items: [Item]?
    let nextPageToken: String?
}

// MARK: - GCS Browser

/// Browser for a publicly-readable Google Cloud Storage bucket.
actor GCSBucketBrowser {
    private let session: URLSession
    private let bucket: String

    init(bucket: String, session: URLSession = .shared) {
        self.bucket = bucket
        self.session = session
    }

    /// Lists up to `maxResults` object names (with optional prefix filtering).
    /// - Parameters:
    ///   - prefix: only return objects whose names begin with this string
    ///   - pageToken: if non-nil, continue listing from this token
    ///   - maxResults: maximum number of results to return (1-1000)
    func listObjects(prefix: String? = nil,
                     pageToken: String? = nil,
                     maxResults: Int = 1000) async throws -> GCSObjectList
    {
        var components = URLComponents()
        components.scheme = "https"
        components.host = "www.googleapis.com"
        components.path = "/storage/v1/b/\(bucket)/o"
        var queryItems: [URLQueryItem] = [
            URLQueryItem(name: "maxResults", value: String(maxResults)),
            // Only grab the fields we actually use
            URLQueryItem(name: "fields", value: "items(name,size,contentType),nextPageToken")
        ]
        if let prefix {
            queryItems.append(.init(name: "prefix", value: prefix))
        }
        if let pageToken {
            queryItems.append(.init(name: "pageToken", value: pageToken))
        }
        components.queryItems = queryItems

        guard let url = components.url else {
            throw URLError(.badURL)
        }

        let (data, response) = try await session.data(from: url)
        guard let http = response as? HTTPURLResponse,
              (200...299).contains(http.statusCode)
        else {
            throw URLError(.badServerResponse)
        }

        return try JSONDecoder().decode(GCSObjectList.self, from: data)
    }

    /// Walks all pages and yields every object name via an async stream.
    func allObjectNames(prefix: String? = nil) -> AsyncThrowingStream<String, Error> {
        AsyncThrowingStream { continuation in
            Task {
                var token: String? = nil
                repeat {
                    let page = try await listObjects(prefix: prefix, pageToken: token)
                    page.items?.forEach { continuation.yield($0.name) }
                    token = page.nextPageToken
                } while token != nil
                continuation.finish()
            }
        }
    }
}

struct ImagesCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "images",
        abstract: "List available edge-agent images in the storage bucket"
    )
    
    @Option(name: .long, help: "Storage bucket name (default: apache-edge-agent-releases)")
    var bucket: String = "edgeos-images-public"
    
    @Option(name: .long, help: "Filter images by architecture (e.g., aarch64, x86_64)")
    var arch: String?
    
    @Option(name: .long, help: "Filter images by version (e.g., v1.0.0)")
    var version: String?
    
    @Flag(name: .long, help: "Show detailed information including size")
    var detailed: Bool = false
    
    func run() async throws {
        let logger = Logger(label: "apache-edge.cli.images")
        logger.info("Listing edge-agent images from bucket: \(bucket)")
        
        let browser = GCSBucketBrowser(bucket: bucket)
        
        // Determine prefix based on filters
        var prefix: String? = nil
        if let version = version {
            if let arch = arch {
                prefix = "edge-agent-\(version)-\(arch)"
            } else {
                prefix = "edge-agent-\(version)"
            }
        } else if let arch = arch {
            prefix = "edge-agent-.*-\(arch)"
        }
        
        // Get the images
        var images: [GCSObjectList.Item] = []
        var hasImages = false
        
        do {
            print("Available edge-agent images:")
            print("----------------------------")
            
            let objectList = try await browser.listObjects(prefix: prefix)
            
            if let items = objectList.items, !items.isEmpty {
                hasImages = true
                images = items
                
                for item in items.sorted(by: { $0.name < $1.name }) {
                    if detailed {
                        let sizeStr = item.size.map { formatFileSize($0) } ?? "unknown size"
                        print("• \(item.name) (\(sizeStr))")
                    } else {
                        print("• \(item.name)")
                    }
                }
            }
            
            if !hasImages {
                print("No images found matching the specified criteria.")
                
                if prefix != nil {
                    print("\nTrying without filters...")
                    let allObjects = try await browser.listObjects()
                    if let items = allObjects.items, !items.isEmpty {
                        for item in items.sorted(by: { $0.name < $1.name }) {
                            print("• \(item.name)")
                        }
                    } else {
                        print("No images found in the bucket.")
                    }
                }
            }
            
        } catch {
            logger.error("Failed to list images: \(error)")
            throw error
        }
    }
    
    private func formatFileSize(_ sizeString: String) -> String {
        guard let sizeBytes = Int64(sizeString) else {
            return sizeString
        }
        
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useKB, .useMB, .useGB]
        formatter.countStyle = .file
        
        return formatter.string(fromByteCount: sizeBytes)
    }
} 