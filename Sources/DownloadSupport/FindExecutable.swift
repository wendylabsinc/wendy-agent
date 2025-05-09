import Foundation

enum FindExecutableError: Error {
    case executableNotFound(String)
}

public func findExecutable(name: String, standardPath: String) throws -> String {
    // Check if unzip is available at the standard locations
    var standardPath = standardPath
    if FileManager.default.fileExists(atPath: standardPath) {
        return standardPath
    }

    standardPath = "/bin/\(name)"
    if FileManager.default.fileExists(atPath: standardPath) {
        return standardPath
    }

    // Try to find unzip in PATH
    let whichUnzip = Process()
    whichUnzip.executableURL = URL(fileURLWithPath: "/bin/sh")
    whichUnzip.arguments = ["-c", "which \(name)"]

    let outputPipe = Pipe()
    whichUnzip.standardOutput = outputPipe

    try whichUnzip.run()
    whichUnzip.waitUntilExit()

    if whichUnzip.terminationStatus == 0 {
        let outputData = outputPipe.fileHandleForReading.readDataToEndOfFile()
        if let path = String(data: outputData, encoding: .utf8)?.trimmingCharacters(
            in: .whitespacesAndNewlines
        ) {
            return path
        }
    }

    throw FindExecutableError.executableNotFound(name)
}
