import ArgumentParser
import Foundation

let e2eTestMetadataFileName = "test.json"
let e2eTestMetadataSchemaID = "wendy.e2e.test.v1"

struct E2ETestMetadata: Codable, Sendable {
    var schema: String
    var sourceFilePath: String
    var sourceFileName: String
    var suiteName: String
    var testName: String
    var functionName: String
    var line: Int
    var declarationLine: Int? = nil
    var sourceStartLine: Int? = nil
    var sourceEndLine: Int? = nil

    var identityKey: E2ETestIdentityKey {
        E2ETestIdentityKey(
            sourceFilePath: sourceFilePath,
            suiteName: suiteName,
            testName: testName
        )
    }

    func validate(sourceURL: URL) throws {
        try validatePath(sourceFilePath, field: "sourceFilePath", sourceURL: sourceURL)
        try validateName(sourceFileName, field: "sourceFileName", sourceURL: sourceURL)
        try validateText(suiteName, field: "suiteName", sourceURL: sourceURL)
        try validateText(testName, field: "testName", sourceURL: sourceURL)
        try validateText(functionName, field: "functionName", sourceURL: sourceURL)
        guard line > 0 else {
            throw ValidationError("Test metadata has invalid line in \(sourceURL.path)")
        }
        try validateOptionalLine(declarationLine, field: "declarationLine", sourceURL: sourceURL)
        try validateOptionalLine(sourceStartLine, field: "sourceStartLine", sourceURL: sourceURL)
        try validateOptionalLine(sourceEndLine, field: "sourceEndLine", sourceURL: sourceURL)
        if let sourceStartLine, let sourceEndLine, sourceStartLine > sourceEndLine {
            throw ValidationError(
                "Test metadata has invalid source line range in \(sourceURL.path)"
            )
        }
        if let declarationLine, let sourceStartLine, declarationLine < sourceStartLine {
            throw ValidationError(
                "Test metadata has declaration before source range in \(sourceURL.path)"
            )
        }
        if let declarationLine, let sourceEndLine, declarationLine > sourceEndLine {
            throw ValidationError(
                "Test metadata has declaration after source range in \(sourceURL.path)"
            )
        }
    }

    private func validateOptionalLine(_ value: Int?, field: String, sourceURL: URL) throws {
        if let value, value <= 0 {
            throw ValidationError("Test metadata has invalid \(field) in \(sourceURL.path)")
        }
    }

    private func validatePath(_ value: String, field: String, sourceURL: URL) throws {
        try validateText(value, field: field, maxLength: 1024, sourceURL: sourceURL)
        guard !value.hasPrefix("/"), !value.split(separator: "/").contains("..") else {
            throw ValidationError("Test metadata has invalid \(field) in \(sourceURL.path)")
        }
        let allowed = CharacterSet(
            charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-"
        )
        guard value.unicodeScalars.allSatisfy({ allowed.contains($0) }) else {
            throw ValidationError(
                "Test metadata has invalid \(field) characters in \(sourceURL.path)"
            )
        }
    }

    private func validateName(_ value: String, field: String, sourceURL: URL) throws {
        try validateText(value, field: field, maxLength: 255, sourceURL: sourceURL)
        let allowed = CharacterSet(
            charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"
        )
        guard value.unicodeScalars.allSatisfy({ allowed.contains($0) }) else {
            throw ValidationError(
                "Test metadata has invalid \(field) characters in \(sourceURL.path)"
            )
        }
    }

    private func validateText(
        _ value: String,
        field: String,
        maxLength: Int = 512,
        sourceURL: URL
    ) throws {
        guard !value.isEmpty, value.count <= maxLength else {
            throw ValidationError("Test metadata has invalid \(field) in \(sourceURL.path)")
        }
    }
}

struct E2ETestIdentityKey: Hashable, Sendable {
    var sourceFilePath: String
    var suiteName: String
    var testName: String
}

func loadE2ETestMetadata(in observationURL: URL) throws -> E2ETestMetadata {
    let url = observationURL.appendingPathComponent(e2eTestMetadataFileName)
    let data = try Data(contentsOf: url)
    let metadata = try JSONDecoder().decode(E2ETestMetadata.self, from: data)
    guard metadata.schema == e2eTestMetadataSchemaID else {
        throw ValidationError("Test metadata has unsupported schema: \(url.path)")
    }
    try metadata.validate(sourceURL: url)
    return metadata
}

func firstE2ETestMetadata(in testObservationURL: URL) throws -> E2ETestMetadata? {
    let directURL = testObservationURL.appendingPathComponent(e2eTestMetadataFileName)
    if FileManager.default.fileExists(atPath: directURL.path) {
        return try loadE2ETestMetadata(in: testObservationURL)
    }

    for targetURL in try e2eMetadataDirectoryChildren(of: testObservationURL) {
        for attemptURL in try e2eMetadataDirectoryChildren(of: targetURL) {
            let metadataURL = attemptURL.appendingPathComponent(e2eTestMetadataFileName)
            if FileManager.default.fileExists(atPath: metadataURL.path) {
                return try loadE2ETestMetadata(in: attemptURL)
            }
        }
    }

    return nil
}

func e2ePackageRelativePath(from packageURL: URL, to url: URL) -> String {
    let basePath = packageURL.standardizedFileURL.path
    let path = url.standardizedFileURL.path
    let prefix = basePath.hasSuffix("/") ? basePath : basePath + "/"
    guard path.hasPrefix(prefix) else {
        return url.lastPathComponent
    }
    return String(path.dropFirst(prefix.count))
}

private func e2eMetadataDirectoryChildren(of url: URL) throws -> [URL] {
    do {
        return try FileManager.default.contentsOfDirectory(
            at: url,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        )
        .filter { (try? $0.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true }
        .sorted { $0.path < $1.path }
    } catch let error as CocoaError where error.code == .fileNoSuchFile {
        return []
    }
}
