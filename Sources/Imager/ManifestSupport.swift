import Foundation

// MARK: - Data Models

/// Represents device manifest information
public struct DeviceManifest: Codable {
    public struct VersionInfo: Codable {
        public let release_date: String
        public let path: String
        public let size_bytes: Int
        public let is_latest: Bool
    }
    
    public let device_id: String
    public let versions: [String: VersionInfo]
}

/// Represents the main manifest containing references to all device manifests
public struct MainManifest: Codable {
    public struct DeviceInfo: Codable {
        public let latest: String
        public let manifest_path: String
    }
    
    public let last_updated: String
    public let devices: [String: DeviceInfo]
}

/// Information about a device from the manifest
public struct DeviceInfo {
    public let name: String
    public let latestVersion: String
    
    public init(name: String, latestVersion: String) {
        self.name = name
        self.latestVersion = latestVersion
    }
}

// MARK: - Protocols

/// Protocol defining manifest management functionality
public protocol ManifestManaging {
    /// Fetches the latest image information for a specific device
    /// - Parameter deviceName: The name of the device
    /// - Returns: The image URL and size
    func getLatestImageInfo(for deviceName: String) async throws -> (url: URL, size: Int)
    
    /// Fetches all available devices from the manifest
    /// - Returns: Array of available device information
    func getAvailableDevices() async throws -> [DeviceInfo]
}

// MARK: - Implementations

/// Manages fetching and parsing device manifests from GCS
public class ManifestManager: ManifestManaging {
    private let baseUrl: String
    private let urlSession: URLSession
    
    public init(baseUrl: String = "https://storage.googleapis.com/edgeos-images-public", urlSession: URLSession = .shared) {
        self.baseUrl = baseUrl
        self.urlSession = urlSession
    }
    
    public func getLatestImageInfo(for deviceName: String) async throws -> (url: URL, size: Int) {
        // Fetch the main manifest
        let mainManifestUrl = URL(string: "\(baseUrl)/manifests/master.json")!
        let (mainManifestData, _) = try await urlSession.data(from: mainManifestUrl)
        let mainManifest = try JSONDecoder().decode(MainManifest.self, from: mainManifestData)
        
        // Find the device in the main manifest
        guard let deviceInfo = mainManifest.devices[deviceName] else {
            throw ManifestError.deviceNotFound(deviceName)
        }
        
        // Check if the device has a manifest path
        guard !deviceInfo.manifest_path.isEmpty else {
            throw ManifestError.noManifestForDevice(deviceName)
        }
        
        // Fetch the device-specific manifest
        let deviceManifestUrl = URL(string: "\(baseUrl)/\(deviceInfo.manifest_path)")!
        let (deviceManifestData, _) = try await urlSession.data(from: deviceManifestUrl)
        let deviceManifest = try JSONDecoder().decode(DeviceManifest.self, from: deviceManifestData)
        
        // Find the latest version
        guard !deviceInfo.latest.isEmpty,
              let versionInfo = deviceManifest.versions[deviceInfo.latest] else {
            throw ManifestError.noLatestVersion(deviceName)
        }
        
        // Get the image URL
        let imageUrl = URL(string: "\(baseUrl)/\(versionInfo.path)")!
        
        return (imageUrl, versionInfo.size_bytes)
    }
    
    public func getAvailableDevices() async throws -> [DeviceInfo] {
        // Fetch the main manifest
        let mainManifestUrl = URL(string: "\(baseUrl)/manifests/master.json")!
        let (mainManifestData, _) = try await urlSession.data(from: mainManifestUrl)
        let mainManifest = try JSONDecoder().decode(MainManifest.self, from: mainManifestData)
        
        // Convert devices dictionary to DeviceInfo array
        return mainManifest.devices.map { (name, info) in
            DeviceInfo(name: name, latestVersion: info.latest)
        }.sorted { $0.name < $1.name }
    }
}

// MARK: - Factory

/// Factory for creating ManifestManager instances
public enum ManifestManagerFactory {
    /// Creates and returns a default ManifestManager instance
    public static func createManifestManager() -> ManifestManaging {
        return ManifestManager()
    }
}

// MARK: - Errors

/// Errors related to manifest operations
public enum ManifestError: Error, LocalizedError {
    case deviceNotFound(String)
    case noManifestForDevice(String)
    case noLatestVersion(String)
    
    public var errorDescription: String? {
        switch self {
        case .deviceNotFound(let deviceName):
            return "Device '\(deviceName)' not found in the manifest"
        case .noManifestForDevice(let deviceName):
            return "No manifest available for device '\(deviceName)'"
        case .noLatestVersion(let deviceName):
            return "No latest version found for device '\(deviceName)'"
        }
    }
} 