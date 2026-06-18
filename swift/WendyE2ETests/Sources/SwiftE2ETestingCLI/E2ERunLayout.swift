import Foundation

let e2eAttemptArtifactsDirectoryName = "attempts"
let e2eObservationsDirectoryName = "observations"
let e2eSourceArtifactFileName = "source.md"
let e2eSourceIndexFileName = "source-index.md"

func e2eAttemptArtifactsRootURL(in runURL: URL) -> URL {
    runURL.appendingPathComponent(e2eAttemptArtifactsDirectoryName, isDirectory: true)
}

func e2eObservationsRootURL(in runURL: URL) -> URL {
    runURL.appendingPathComponent(e2eObservationsDirectoryName, isDirectory: true)
}

func e2eAttemptArtifactsURL(in runURL: URL, targetName: String, attempt: String) -> URL {
    e2eAttemptArtifactsRootURL(in: runURL)
        .appendingPathComponent(targetName, isDirectory: true)
        .appendingPathComponent(attempt, isDirectory: true)
}

func e2eAttemptExitStatus(at attemptURL: URL) -> Int? {
    let url = attemptURL.appendingPathComponent("attempt.json")
    guard FileManager.default.fileExists(atPath: url.path),
        let data = try? Data(contentsOf: url),
        let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
    else {
        return nil
    }
    return object["exitStatus"] as? Int
}
