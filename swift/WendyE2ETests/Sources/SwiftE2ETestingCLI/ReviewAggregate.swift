import Foundation

func writeE2EReviewAggregate(in runURL: URL) throws {
    let issues = try loadE2EReviewAggregateIssues(in: runURL)
    let overview = try loadRunOverview(in: runURL)
    let markdown = renderE2EReviewAggregate(issues: issues, overview: overview)
    let outputURL = runURL.appendingPathComponent("review.md")
    try markdown.write(to: outputURL, atomically: true, encoding: .utf8)
    print("==> Wrote Swift E2E review aggregate")
    print("    Review: \(outputURL.path)")
}

private struct E2EReviewAggregateIssue {
    var scope: E2EReviewAggregateScope
    var suiteKey: String?
    var testKey: String?
    var review: E2EReview

    var severity: E2EReviewSeverity {
        review.metadata.severity
    }
}

private enum E2EReviewAggregateScope: Int {
    case report = 0
    case suite = 1
    case test = 2

    var metadataValue: String {
        switch self {
        case .report:
            "run"
        case .suite:
            "suite"
        case .test:
            "test"
        }
    }
}

private func loadE2EReviewAggregateIssues(in runURL: URL) throws -> [E2EReviewAggregateIssue] {
    var issues = try loadE2EReviews(
        in: runURL,
        expectedScope: "report",
        relativeTo: runURL
    ).map { review in
        E2EReviewAggregateIssue(scope: .report, suiteKey: nil, testKey: nil, review: review)
    }

    for suiteURL in try reviewAggregateDirectoryChildren(of: runURL) {
        let suiteKey = suiteURL.lastPathComponent
        guard !isE2EReviewDirectoryName(suiteKey) else { continue }

        issues += try loadE2EReviews(
            in: suiteURL,
            expectedScope: "suite",
            relativeTo: runURL
        ).map { review in
            E2EReviewAggregateIssue(scope: .suite, suiteKey: suiteKey, testKey: nil, review: review)
        }

        for testURL in try reviewAggregateDirectoryChildren(of: suiteURL) {
            let testKey = testURL.lastPathComponent
            guard !isE2EReviewDirectoryName(testKey) else { continue }

            issues += try loadE2EReviews(
                in: testURL,
                expectedScope: "test",
                relativeTo: runURL
            ).map { review in
                E2EReviewAggregateIssue(
                    scope: .test,
                    suiteKey: suiteKey,
                    testKey: testKey,
                    review: review
                )
            }
        }
    }

    return issues.sorted { lhs, rhs in
        if lhs.severity.sortRank != rhs.severity.sortRank {
            return lhs.severity.sortRank < rhs.severity.sortRank
        }
        if lhs.scope.rawValue != rhs.scope.rawValue {
            return lhs.scope.rawValue < rhs.scope.rawValue
        }
        if lhs.suiteKey != rhs.suiteKey {
            return (lhs.suiteKey ?? "") < (rhs.suiteKey ?? "")
        }
        if lhs.testKey != rhs.testKey {
            return (lhs.testKey ?? "") < (rhs.testKey ?? "")
        }
        return lhs.review.path < rhs.review.path
    }
}

private func renderE2EReviewAggregate(
    issues: [E2EReviewAggregateIssue],
    overview: E2ERunOverview?
) -> String {
    var lines: [String] = [
        "# Swift E2E Review",
        "",
    ]

    let wroteOutcomeSummary = appendE2EReviewAggregateOutcomeSummary(
        overview: overview,
        issues: issues,
        to: &lines
    )

    guard !issues.isEmpty else {
        if !wroteOutcomeSummary {
            lines.append("No Swift E2E review issues were generated for this run.")
            lines.append("")
        }
        return lines.joined(separator: "\n")
    }

    let runIssues =
        issues
        .filter { $0.scope == .report }
        .sorted(by: reviewAggregateIssueSort)
    if wroteOutcomeSummary, !runIssues.isEmpty {
        lines.append("---")
        lines.append("")
    }
    for issue in runIssues {
        appendE2EReviewAggregateIssue(issue, headingLevel: 2, to: &lines)
    }

    var wroteSuite = false
    let suiteKeys = Set(issues.compactMap(\.suiteKey)).sorted()
    for suiteKey in suiteKeys {
        let suiteIssues =
            issues
            .filter { $0.scope == .suite && $0.suiteKey == suiteKey }
            .sorted(by: reviewAggregateIssueSort)
        let testIssues =
            issues
            .filter { $0.scope == .test && $0.suiteKey == suiteKey }
        guard !suiteIssues.isEmpty || !testIssues.isEmpty else { continue }

        if wroteOutcomeSummary || !runIssues.isEmpty || wroteSuite {
            lines.append("---")
            lines.append("")
        }
        lines.append("## `\(suiteKey)`")
        lines.append("")

        for issue in suiteIssues {
            appendE2EReviewAggregateIssue(issue, headingLevel: 3, to: &lines)
        }
        for issue in testIssues.sorted(by: reviewAggregateIssueSort) {
            appendE2EReviewAggregateIssue(issue, headingLevel: 3, to: &lines)
        }

        wroteSuite = true
    }

    return lines.joined(separator: "\n")
}

private func reviewAggregateIssueSort(
    _ lhs: E2EReviewAggregateIssue,
    _ rhs: E2EReviewAggregateIssue
) -> Bool {
    if lhs.severity.sortRank != rhs.severity.sortRank {
        return lhs.severity.sortRank < rhs.severity.sortRank
    }
    if lhs.review.title != rhs.review.title {
        return lhs.review.title < rhs.review.title
    }
    return lhs.review.path < rhs.review.path
}

@discardableResult
private func appendE2EReviewAggregateOutcomeSummary(
    overview: E2ERunOverview?,
    issues: [E2EReviewAggregateIssue],
    to lines: inout [String]
) -> Bool {
    guard let overview else { return false }

    let failures = overview.noteworthy.deterministicFailures
    let flakes = overview.noteworthy.flakes
    guard !failures.isEmpty || !flakes.isEmpty else { return false }

    lines.append("## Failed and flaked tests")
    lines.append("")
    lines.append(
        "Every failed or flaked target outcome is listed here with the matching AI review evidence when one was recorded. Failed tests should identify the likely root cause and next action; flaked tests should explain why the outcome may be nondeterministic and what to do next."
    )
    lines.append("")

    for issue in failures.sorted(by: reviewAggregateOverviewIssueSort) {
        appendE2EReviewAggregateOutcome(
            issue,
            label: "Failed",
            marker: "🛑",
            relatedReviews: relatedReviews(for: issue, in: issues),
            to: &lines
        )
    }
    for issue in flakes.sorted(by: reviewAggregateOverviewIssueSort) {
        appendE2EReviewAggregateOutcome(
            issue,
            label: "Flaked",
            marker: "⚠️",
            relatedReviews: relatedReviews(for: issue, in: issues),
            to: &lines
        )
    }

    return true
}

private func appendE2EReviewAggregateOutcome(
    _ issue: E2ERunOverviewIssue,
    label: String,
    marker: String,
    relatedReviews: [E2EReviewAggregateIssue],
    to lines: inout [String]
) {
    let title = "`\(issue.suite)/\(issue.test)` on `\(issue.target)` \(label.lowercased())"
    lines.append("### \(marker) \(title)")
    lines.append("")
    if relatedReviews.isEmpty {
        lines.append(
            "No AI review file was recorded for this \(label.lowercased()) target outcome. Add a review that explains the likely root cause and the next action."
        )
    } else {
        for reviewIssue in relatedReviews {
            let review = reviewIssue.review
            lines.append("- AI review: **\(reviewAggregateSingleLine(review.title))**")
            lines.append("")
            lines.append(reviewAggregateSummaryMarkdown(review.summaryMarkdown))
            lines.append("")
        }
    }

    lines.append("<details>")
    lines.append("<summary>Outcome evidence</summary>")
    lines.append("")
    lines.append("- Outcome: `\(issue.outcome.rawValue)`")
    lines.append("- Target: `\(issue.target)`")
    lines.append("- Attempts: \(reviewAggregateAttemptSummary(issue.attempts))")
    for attempt in issue.attempts where attempt.status != .passed || issue.outcome == .flaked {
        lines.append("  - `\(attempt.attempt)`: `\(attempt.status.rawValue)`")
        if let detail = attempt.detail, !detail.isEmpty {
            lines.append("    - Detail: \(reviewAggregateSingleLine(detail))")
        }
        appendE2EReviewAggregateEvidence(attempt.artifacts, to: &lines)
    }
    if !relatedReviews.isEmpty {
        lines.append("- Related review files:")
        for reviewIssue in relatedReviews {
            lines.append("  - `\(reviewIssue.review.path)`")
        }
    }
    lines.append("")
    lines.append("</details>")
    lines.append("")
}

private func reviewAggregateOverviewIssueSort(
    _ lhs: E2ERunOverviewIssue,
    _ rhs: E2ERunOverviewIssue
) -> Bool {
    if lhs.suite != rhs.suite { return lhs.suite < rhs.suite }
    if lhs.test != rhs.test { return lhs.test < rhs.test }
    return lhs.target < rhs.target
}

private func relatedReviews(
    for issue: E2ERunOverviewIssue,
    in reviews: [E2EReviewAggregateIssue]
) -> [E2EReviewAggregateIssue] {
    let exact = reviews.filter { review in
        review.scope == .test && review.suiteKey == issue.suite && review.testKey == issue.test
    }
    if !exact.isEmpty {
        return exact.sorted(by: reviewAggregateIssueSort)
    }

    return reviews.filter { review in
        review.scope == .suite && review.suiteKey == issue.suite
    }
    .sorted(by: reviewAggregateIssueSort)
}

private func appendE2EReviewAggregateEvidence(
    _ artifacts: E2ERunOverviewArtifacts,
    to lines: inout [String]
) {
    if let recording = artifacts.recording {
        lines.append("    - Recording: `\(recording)`")
    }
    if let shell = artifacts.shell {
        lines.append("    - Shell: `\(shell)`")
    }
    if let testResults = artifacts.testResults {
        lines.append("    - xUnit: `\(testResults)`")
    }
}

private func reviewAggregateAttemptSummary(_ attempts: [E2ERunOverviewIssueAttempt]) -> String {
    attempts.map { "`\($0.attempt):\($0.status.rawValue)`" }.joined(separator: ", ")
}

private func reviewAggregateSeverityMarker(_ severity: E2EReviewSeverity) -> String {
    severity.symbol
}

private func appendE2EReviewAggregateIssue(
    _ issue: E2EReviewAggregateIssue,
    headingLevel: Int,
    to lines: inout [String]
) {
    let review = issue.review
    lines.append(reviewAggregateTitleLine(for: issue, headingLevel: headingLevel))
    lines.append("")
    lines.append(reviewAggregateSummaryMarkdown(review.summaryMarkdown))
    lines.append("")
    lines.append("<details>")
    lines.append("<summary>Details</summary>")
    lines.append("")
    lines.append(review.detailsMarkdown)
    lines.append("")
    appendE2EReviewAggregateMetadata(issue, to: &lines)
    lines.append("")
    lines.append("</details>")
    lines.append("")
}

private func reviewAggregateTitleLine(
    for issue: E2EReviewAggregateIssue,
    headingLevel: Int
) -> String {
    let heading = String(repeating: "#", count: headingLevel)
    let title = reviewAggregateSingleLine(issue.review.title)
    return "\(heading) \(reviewAggregateSeverityMarker(issue.severity)) \(title)"
}

private func appendE2EReviewAggregateMetadata(
    _ issue: E2EReviewAggregateIssue,
    to lines: inout [String]
) {
    let review = issue.review
    lines.append("- Scope: `\(issue.scope.metadataValue)`")
    if let testKey = issue.testKey {
        lines.append("- Test: `\(testKey)`")
    }
    lines.append("- Reviewer: `\(review.metadata.reviewer)`")
    if let confidence = review.metadata.confidence, !confidence.isEmpty {
        lines.append("- Confidence: `\(confidence)`")
    }
    if let locations = review.metadata.locations, !locations.isEmpty {
        lines.append("- Locations: \(reviewAggregateLocations(locations))")
    }
    lines.append("- Full review: `\(review.path)`")
}

private func reviewAggregateLocations(_ locations: [E2EReviewLocation]) -> String {
    locations.map { location in
        var value = "\(location.path):\(location.startLine)"
        if let endLine = location.endLine, endLine != location.startLine {
            value += "-\(endLine)"
        }
        return "`\(value)`"
    }.joined(separator: ", ")
}

private func reviewAggregateDirectoryChildren(of url: URL) throws -> [URL] {
    guard FileManager.default.fileExists(atPath: url.path) else { return [] }
    return try FileManager.default.contentsOfDirectory(
        at: url,
        includingPropertiesForKeys: [.isDirectoryKey],
        options: [.skipsHiddenFiles]
    )
    .filter { child in
        ((try? child.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true)
    }
    .sorted { $0.lastPathComponent < $1.lastPathComponent }
}

private func reviewAggregateSingleLine(_ value: String) -> String {
    value
        .replacingOccurrences(of: "\r", with: " ")
        .replacingOccurrences(of: "\n", with: " ")
        .trimmingCharacters(in: .whitespacesAndNewlines)
}

private func reviewAggregateSummaryMarkdown(_ value: String) -> String {
    let original = value.trimmingCharacters(in: .whitespacesAndNewlines)
    let patterns = [
        #"^(?:\*\*|__)?\s*(?:🛑|⚠️|💡)\s*(?:(?:Error|Fail(?:ure)?|Concern|Info)\b)?\s*(?::|[-–—])?\s*(?:\*\*|__)?\s*"#,
        #"^(?:\*\*|__)?\s*(?:Error|Fail(?:ure)?|Concern|Info)\b\s*(?::|[-–—])\s*(?:\*\*|__)?\s*"#,
    ]

    for pattern in patterns {
        guard let regex = try? NSRegularExpression(pattern: pattern, options: [.caseInsensitive]) else {
            continue
        }
        let range = NSRange(original.startIndex..<original.endIndex, in: original)
        guard let match = regex.firstMatch(in: original, range: range), match.range.location == 0,
            match.range.length > 0,
            let swiftRange = Range(match.range, in: original)
        else {
            continue
        }

        let stripped = String(original[swiftRange.upperBound...])
            .trimmingCharacters(in: .whitespacesAndNewlines)
        if !stripped.isEmpty {
            return stripped
        }
    }

    return original
}
