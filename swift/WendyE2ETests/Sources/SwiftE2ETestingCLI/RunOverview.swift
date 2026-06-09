import ArgumentParser
import Foundation

#if canImport(FoundationXML)
    import FoundationXML
#endif

let e2eRunOverviewFileName = "overview.json"
let e2eRunOverviewSchemaID = "wendy.e2e.overview.v1"

struct E2ERunOverview: Codable, Sendable {
    var schema: String
    var generatedAt: String
    var summary: E2ERunOverviewSummary
    var targets: [E2ERunOverviewTarget]
    var noteworthy: E2ERunOverviewNoteworthy
}

struct E2ERunOverviewSummary: Codable, Sendable {
    var tests: Int
    var testTargets: Int
    var attemptResults: Int
    var commands: Int
    var passed: Int
    var flaked: Int
    var failed: Int
    var skipped: Int
    var unknown: Int
}

struct E2ERunOverviewTarget: Codable, Sendable {
    var name: String
    var outcome: E2ERunOverviewOutcome
    var attempts: Int
    var tests: Int
    var passed: Int
    var flaked: Int
    var failed: Int
    var skipped: Int
    var unknown: Int
}

struct E2ERunOverviewArtifacts: Codable, Sendable {
    var recording: String?
    var shell: String?
    var testResults: String?
}

struct E2ERunOverviewNoteworthy: Codable, Sendable {
    var deterministicFailures: [E2ERunOverviewIssue]
    var flakes: [E2ERunOverviewIssue]
    var unknowns: [E2ERunOverviewIssue]
}

struct E2ERunOverviewIssue: Codable, Sendable {
    var suite: String
    var test: String
    var target: String
    var outcome: E2ERunOverviewOutcome
    var attempts: [E2ERunOverviewIssueAttempt]
}

struct E2ERunOverviewIssueAttempt: Codable, Sendable {
    var attempt: String
    var status: E2ERunOverviewStatus
    var durationSeconds: Double?
    var detail: String?
    var artifacts: E2ERunOverviewArtifacts
}

enum E2ERunOverviewOutcome: String, Codable, Sendable {
    case passed = "PASSED"
    case flaked = "FLAKED"
    case failed = "FAILED"
    case skipped = "SKIPPED"
    case unknown = "UNKNOWN"
}

enum E2ERunOverviewStatus: String, Codable, Sendable {
    case passed = "PASSED"
    case failed = "FAILED"
    case skipped = "SKIPPED"
    case unknown = "UNKNOWN"
}

@discardableResult
func writeRunOverview(in runURL: URL) throws -> E2ERunOverview {
    let overview = try makeRunOverview(in: runURL)
    let outputURL = runOverviewURL(in: runURL)
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
    let data = try encoder.encode(overview)
    try data.write(to: outputURL, options: .atomic)
    return overview
}

func loadRunOverview(in runURL: URL) throws -> E2ERunOverview? {
    let url = runOverviewURL(in: runURL)
    guard FileManager.default.fileExists(atPath: url.path) else {
        return nil
    }
    let data = try Data(contentsOf: url)
    let overview = try JSONDecoder().decode(E2ERunOverview.self, from: data)
    guard overview.schema == e2eRunOverviewSchemaID else {
        throw ValidationError("Run overview has unsupported schema: \(url.path)")
    }
    return overview
}

func ensureRunOverview(in runURL: URL) throws -> E2ERunOverview {
    if let overview = try loadRunOverview(in: runURL) {
        return overview
    }
    return try writeRunOverview(in: runURL)
}

func runOverviewURL(in runURL: URL) -> URL {
    runURL.appendingPathComponent(e2eRunOverviewFileName)
}

private struct OverviewResultKey: Hashable {
    var suite: String
    var name: String
}

private struct OverviewObservationResult {
    var status: E2ERunOverviewStatus
    var durationSeconds: Double?
    var detail: String?
}

private struct OverviewOutcomeCounts {
    var passed = 0
    var flaked = 0
    var failed = 0
    var skipped = 0
    var unknown = 0

    mutating func add(_ outcome: E2ERunOverviewOutcome) {
        switch outcome {
        case .passed:
            passed += 1
        case .flaked:
            flaked += 1
        case .failed:
            failed += 1
        case .skipped:
            skipped += 1
        case .unknown:
            unknown += 1
        }
    }
}

private struct OverviewTargetAccumulator {
    var attempts: Set<String> = []
    var tests = 0
    var counts = OverviewOutcomeCounts()
}

private func makeRunOverview(in runURL: URL) throws -> E2ERunOverview {
    var targetAccumulators: [String: OverviewTargetAccumulator] = [:]
    var summaryCounts = OverviewOutcomeCounts()
    var uniqueTests = Set<String>()
    var testTargetCount = 0
    var attemptResultCount = 0
    var commandCount = 0
    var deterministicFailures: [E2ERunOverviewIssue] = []
    var flakes: [E2ERunOverviewIssue] = []
    var unknowns: [E2ERunOverviewIssue] = []

    for suiteURL in try overviewDirectoryChildren(of: runURL) {
        let suiteKey = suiteURL.lastPathComponent
        guard !isE2EReviewDirectoryName(suiteKey) else { continue }

        for testURL in try overviewDirectoryChildren(of: suiteURL) {
            let testKey = testURL.lastPathComponent
            guard !isE2EReviewDirectoryName(testKey) else { continue }
            var hasTargetOutcome = false

            for targetURL in try overviewDirectoryChildren(of: testURL) {
                let targetName = targetURL.lastPathComponent
                guard !isE2EReviewDirectoryName(targetName) else { continue }
                var attempts: [E2ERunOverviewIssueAttempt] = []

                for attemptURL in try overviewDirectoryChildren(of: targetURL) {
                    let attemptName = attemptURL.lastPathComponent
                    guard !isE2EReviewDirectoryName(attemptName) else { continue }
                    let result = try overviewObservationResult(
                        suiteKey: suiteKey,
                        testKey: testKey,
                        attemptURL: attemptURL
                    )
                    let artifacts = overviewArtifacts(attemptURL: attemptURL, runURL: runURL)
                    attempts.append(
                        E2ERunOverviewIssueAttempt(
                            attempt: attemptName,
                            status: result.status,
                            durationSeconds: result.durationSeconds,
                            detail: result.detail,
                            artifacts: artifacts
                        )
                    )
                    attemptResultCount += 1
                    commandCount += try overviewCommandCount(attemptURL: attemptURL)
                }

                attempts.sort { $0.attempt < $1.attempt }
                let outcome = overviewOutcome(for: attempts.map(\.status))
                hasTargetOutcome = true

                testTargetCount += 1
                summaryCounts.add(outcome)
                var targetAccumulator = targetAccumulators[
                    targetName,
                    default: OverviewTargetAccumulator()
                ]
                targetAccumulator.attempts.formUnion(attempts.map(\.attempt))
                targetAccumulator.tests += 1
                targetAccumulator.counts.add(outcome)
                targetAccumulators[targetName] = targetAccumulator

                let issue = E2ERunOverviewIssue(
                    suite: suiteKey,
                    test: testKey,
                    target: targetName,
                    outcome: outcome,
                    attempts: attempts
                )
                switch outcome {
                case .failed:
                    deterministicFailures.append(issue)
                case .flaked:
                    flakes.append(issue)
                case .unknown:
                    unknowns.append(issue)
                case .passed, .skipped:
                    break
                }
            }

            if hasTargetOutcome {
                uniqueTests.insert("\(suiteKey)/\(testKey)")
            }
        }
    }

    let targets = targetAccumulators.map { targetName, accumulator in
        E2ERunOverviewTarget(
            name: targetName,
            outcome: overviewTargetOutcome(for: accumulator.counts),
            attempts: accumulator.attempts.count,
            tests: accumulator.tests,
            passed: accumulator.counts.passed,
            flaked: accumulator.counts.flaked,
            failed: accumulator.counts.failed,
            skipped: accumulator.counts.skipped,
            unknown: accumulator.counts.unknown
        )
    }.sorted { $0.name < $1.name }

    return E2ERunOverview(
        schema: e2eRunOverviewSchemaID,
        generatedAt: ISO8601DateFormatter().string(from: Date()),
        summary: E2ERunOverviewSummary(
            tests: uniqueTests.count,
            testTargets: testTargetCount,
            attemptResults: attemptResultCount,
            commands: commandCount,
            passed: summaryCounts.passed,
            flaked: summaryCounts.flaked,
            failed: summaryCounts.failed,
            skipped: summaryCounts.skipped,
            unknown: summaryCounts.unknown
        ),
        targets: targets,
        noteworthy: E2ERunOverviewNoteworthy(
            deterministicFailures: deterministicFailures.sorted(by: overviewIssueSort),
            flakes: flakes.sorted(by: overviewIssueSort),
            unknowns: unknowns.sorted(by: overviewIssueSort)
        )
    )
}

private func overviewObservationResult(
    suiteKey: String,
    testKey: String,
    attemptURL: URL
) throws -> OverviewObservationResult {
    let resultURL = attemptURL.appendingPathComponent("test-results.xml")
    guard FileManager.default.fileExists(atPath: resultURL.path) else {
        return OverviewObservationResult(
            status: .unknown,
            durationSeconds: nil,
            detail: "test-results.xml not found"
        )
    }

    let results: [OverviewResultKey: OverviewObservationResult]
    do {
        results = try overviewParseXUnitResults(at: resultURL)
    } catch {
        return OverviewObservationResult(
            status: .unknown,
            durationSeconds: nil,
            detail: "Could not parse test-results.xml: \(error)"
        )
    }

    if let result = results.first(where: { key, _ in
        overviewSlug(key.suite) == suiteKey && overviewSlug(key.name) == testKey
    })?.value {
        return result
    }

    let matchingTestNames = results.filter { key, _ in
        overviewSlug(key.name) == testKey
    }
    if matchingTestNames.count == 1, let result = matchingTestNames.first?.value {
        return result
    }

    return OverviewObservationResult(
        status: .unknown,
        durationSeconds: nil,
        detail: "No Swift Testing result was found for this test in test-results.xml"
    )
}

private func overviewParseXUnitResults(
    at resultURL: URL
) throws -> [OverviewResultKey: OverviewObservationResult] {
    let data = try Data(contentsOf: resultURL)
    let parser = OverviewXUnitResultParser()
    let xmlParser = XMLParser(data: data)
    xmlParser.delegate = parser
    guard xmlParser.parse() else {
        throw ValidationError("Could not parse Swift Testing xUnit results: \(resultURL.path)")
    }
    return parser.results
}

private func overviewArtifacts(attemptURL: URL, runURL: URL) -> E2ERunOverviewArtifacts {
    E2ERunOverviewArtifacts(
        recording: overviewRelativeFilePath(
            fileName: "recording.md",
            attemptURL: attemptURL,
            runURL: runURL
        ),
        shell: overviewRelativeFilePath(
            fileName: "recording.sh.txt",
            attemptURL: attemptURL,
            runURL: runURL
        ),
        testResults: overviewRelativeFilePath(
            fileName: "test-results.xml",
            attemptURL: attemptURL,
            runURL: runURL
        )
    )
}

private func overviewRelativeFilePath(fileName: String, attemptURL: URL, runURL: URL) -> String? {
    let url = attemptURL.appendingPathComponent(fileName)
    guard FileManager.default.fileExists(atPath: url.path) else { return nil }
    return overviewRelativePath(from: runURL, to: url)
}

private func overviewRelativePath(from baseURL: URL, to url: URL) -> String {
    let basePath = baseURL.standardizedFileURL.path
    let path = url.standardizedFileURL.path
    let prefix = basePath.hasSuffix("/") ? basePath : basePath + "/"
    guard path.hasPrefix(prefix) else { return url.lastPathComponent }
    return String(path.dropFirst(prefix.count))
}

private func overviewCommandCount(attemptURL: URL) throws -> Int {
    let recordURL = attemptURL.appendingPathComponent("recording.md")
    guard FileManager.default.fileExists(atPath: recordURL.path) else { return 0 }
    let text = try String(contentsOf: recordURL, encoding: .utf8)
    return text.components(separatedBy: "\n## Command").count - 1
}

private func overviewOutcome(for statuses: [E2ERunOverviewStatus]) -> E2ERunOverviewOutcome {
    guard !statuses.isEmpty else { return .unknown }

    let passed = statuses.contains(.passed)
    let failed = statuses.contains(.failed)
    let skipped = statuses.contains(.skipped)
    let unknown = statuses.contains(.unknown)

    if skipped && !passed && !failed && !unknown {
        return .skipped
    }
    if passed && failed {
        return .flaked
    }
    if passed && !failed && !skipped && !unknown {
        return .passed
    }
    if failed && !passed && !skipped && !unknown {
        return .failed
    }
    return .unknown
}

private func overviewTargetOutcome(for counts: OverviewOutcomeCounts) -> E2ERunOverviewOutcome {
    if counts.failed > 0 { return .failed }
    if counts.unknown > 0 { return .unknown }
    if counts.flaked > 0 { return .flaked }
    if counts.passed > 0 { return .passed }
    if counts.skipped > 0 { return .skipped }
    return .unknown
}

private func overviewIssueSort(
    _ lhs: E2ERunOverviewIssue,
    _ rhs: E2ERunOverviewIssue
) -> Bool {
    if lhs.suite != rhs.suite { return lhs.suite < rhs.suite }
    if lhs.test != rhs.test { return lhs.test < rhs.test }
    return lhs.target < rhs.target
}

private func overviewDirectoryChildren(of url: URL) throws -> [URL] {
    guard FileManager.default.fileExists(atPath: url.path) else { return [] }
    return try FileManager.default.contentsOfDirectory(
        at: url,
        includingPropertiesForKeys: [.isDirectoryKey],
        options: [.skipsHiddenFiles]
    )
    .filter { (try? $0.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true }
    .sorted { $0.path < $1.path }
}

private final class OverviewXUnitResultParser: NSObject, XMLParserDelegate {
    var results: [OverviewResultKey: OverviewObservationResult] = [:]

    private var current:
        (key: OverviewResultKey, failure: String?, skipped: String?, time: Double?)?
    private var currentElement: String?
    private var currentText = ""

    func parser(
        _: XMLParser,
        didStartElement elementName: String,
        namespaceURI _: String?,
        qualifiedName _: String?,
        attributes attributeDict: [String: String]
    ) {
        switch elementName {
        case "testcase":
            guard let classname = attributeDict["classname"], let name = attributeDict["name"],
                let key = overviewTestResultKey(classname: classname, name: name)
            else {
                current = nil
                return
            }
            current = (
                key: key,
                failure: nil,
                skipped: nil,
                time: attributeDict["time"].flatMap(Double.init)
            )
        case "failure", "skipped":
            currentElement = elementName
            currentText = ""
            guard var current else { return }
            if elementName == "failure" {
                current.failure = attributeDict["message"] ?? ""
            } else {
                current.skipped = attributeDict["message"] ?? ""
            }
            self.current = current
        default:
            break
        }
    }

    func parser(_: XMLParser, foundCharacters string: String) {
        if currentElement == "failure" || currentElement == "skipped" {
            currentText.append(string)
        }
    }

    func parser(
        _: XMLParser,
        didEndElement elementName: String,
        namespaceURI _: String?,
        qualifiedName _: String?
    ) {
        switch elementName {
        case "failure", "skipped":
            guard var current else { return }
            let text = currentText.trimmingCharacters(in: .whitespacesAndNewlines)
            if elementName == "failure", current.failure?.isEmpty != false, !text.isEmpty {
                current.failure = text
            } else if elementName == "skipped", current.skipped?.isEmpty != false, !text.isEmpty {
                current.skipped = text
            }
            self.current = current
            currentElement = nil
            currentText = ""
        case "testcase":
            guard let current else { return }
            let result: OverviewObservationResult
            if let skipped = current.skipped {
                result = OverviewObservationResult(
                    status: .skipped,
                    durationSeconds: current.time,
                    detail: skipped.isEmpty ? nil : skipped
                )
            } else if let failure = current.failure {
                result = OverviewObservationResult(
                    status: .failed,
                    durationSeconds: current.time,
                    detail: failure.isEmpty ? nil : failure
                )
            } else {
                result = OverviewObservationResult(
                    status: .passed,
                    durationSeconds: current.time,
                    detail: nil
                )
            }
            results[current.key] = result
            self.current = nil
        default:
            break
        }
    }
}

private func overviewTestResultKey(classname: String, name: String) -> OverviewResultKey? {
    let suite = overviewNormalizedClassname(classname)
    let testName = overviewNormalizedTestName(name)
    guard !suite.isEmpty, !testName.isEmpty else { return nil }
    return OverviewResultKey(suite: suite, name: testName)
}

private func overviewNormalizedClassname(_ classname: String) -> String {
    if classname.last == "`", let start = classname.dropLast().lastIndex(of: "`") {
        let suiteStart = classname.index(after: start)
        return String(classname[suiteStart..<classname.index(before: classname.endIndex)])
    }
    return overviewStripBackticks(String(classname.split(separator: ".").last ?? ""))
}

private func overviewNormalizedTestName(_ name: String) -> String {
    var value = name
    if value.hasSuffix("()") {
        value.removeLast(2)
    }
    return overviewStripBackticks(value)
}

private func overviewStripBackticks(_ value: String) -> String {
    if value.first == "`", value.last == "`" {
        return String(value.dropFirst().dropLast())
    }
    return value
}

private func overviewSlug(_ value: String) -> String {
    var result = ""
    var needsSeparator = false
    var previousKind: OverviewSlugCharacterKind?
    let scalars = Array(value.unicodeScalars)

    for index in scalars.indices {
        let scalar = scalars[index]
        guard let kind = OverviewSlugCharacterKind(scalar) else {
            needsSeparator = !result.isEmpty
            previousKind = nil
            continue
        }
        let nextKind =
            scalars.index(after: index) < scalars.endIndex
            ? OverviewSlugCharacterKind(scalars[scalars.index(after: index)]) : nil
        if !result.isEmpty,
            needsSeparator
                || overviewNeedsCamelCaseSeparator(
                    previousKind: previousKind,
                    currentKind: kind,
                    nextKind: nextKind
                )
        {
            result.append("-")
        }
        result.append(Character(scalar).lowercased())
        needsSeparator = false
        previousKind = kind
    }

    return result
}

private enum OverviewSlugCharacterKind {
    case uppercase
    case lowercase
    case digit

    init?(_ scalar: UnicodeScalar) {
        switch scalar.value {
        case 48...57:
            self = .digit
        case 65...90:
            self = .uppercase
        case 97...122:
            self = .lowercase
        default:
            return nil
        }
    }
}

private func overviewNeedsCamelCaseSeparator(
    previousKind: OverviewSlugCharacterKind?,
    currentKind: OverviewSlugCharacterKind,
    nextKind: OverviewSlugCharacterKind?
) -> Bool {
    guard let previousKind else { return false }
    if currentKind == .digit {
        return previousKind != .digit
    }
    if previousKind == .digit {
        return true
    }
    if currentKind == .uppercase, previousKind == .lowercase {
        return true
    }
    if currentKind == .uppercase, previousKind == .uppercase, nextKind == .lowercase {
        return true
    }
    return false
}
