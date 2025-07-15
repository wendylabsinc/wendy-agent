import Foundation

/// Protocol for abstracting file system operations to enable testing
public protocol FileSystemProvider: Sendable {
    func fileExists(atPath path: String) -> Bool
    func contentsOfDirectory(atPath path: String) throws -> [String]
    func readFile(atPath path: String) throws -> String?
}

/// Default implementation using Foundation's FileManager
public struct DefaultFileSystemProvider: FileSystemProvider {

    public init() {}

    public func fileExists(atPath path: String) -> Bool {
        return FileManager.default.fileExists(atPath: path)
    }

    public func contentsOfDirectory(atPath path: String) throws -> [String] {
        return try FileManager.default.contentsOfDirectory(atPath: path)
    }

    public func readFile(atPath path: String) throws -> String? {
        do {
            let content = try String(contentsOfFile: path, encoding: .utf8)
            return content.trimmingCharacters(in: .whitespacesAndNewlines)
        } catch {
            return nil
        }
    }
}
