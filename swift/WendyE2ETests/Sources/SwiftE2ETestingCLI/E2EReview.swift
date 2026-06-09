import ArgumentParser
import Foundation

let e2eReviewSchemaID = "wendy.e2e.review.v1"

struct E2EReview: Sendable {
    var metadata: E2EReviewMetadata
    var path: String
    var title: String
    var summaryMarkdown: String
    var detailsMarkdown: String
}

struct E2EReviewMetadata: Codable, Sendable {
    var schema: String
    var title: String
    var scope: String
    var reviewer: String
    var severity: E2EReviewSeverity
    var confidence: String?
    var locations: [E2EReviewLocation]?
    var evidence: [E2EReviewEvidence]?
}

enum E2EReviewSeverity: String, Codable, Sendable {
    case fail
    case concern
    case info

    var sortRank: Int {
        switch self {
        case .fail:
            0
        case .concern:
            1
        case .info:
            2
        }
    }

    var displayName: String {
        switch self {
        case .fail:
            "🛑 Error"
        case .concern:
            "⚠️ Concern"
        case .info:
            "💡 Info"
        }
    }

    var symbol: String {
        switch self {
        case .fail:
            "🛑"
        case .concern:
            "⚠️"
        case .info:
            "💡"
        }
    }
}

struct E2EReviewLocation: Codable, Sendable {
    var path: String
    var startLine: Int
    var endLine: Int?
}

struct E2EReviewEvidence: Codable, Sendable {
    var path: String
    var title: String?
}

func e2eReviewReviewer(model: String) -> String {
    let modelSlug = e2eReviewSlug(model)
    guard modelSlug != "review", modelSlug != "default", modelSlug != "latest" else {
        return "default"
    }
    return modelSlug
}

func e2eReviewDirectoryName(reviewer: String) -> String {
    "review.\(reviewer)"
}

func isE2EReviewDirectoryName(_ name: String) -> Bool {
    name.hasPrefix("review.") && name.count > "review.".count
}

func loadE2EReviews(
    in directoryURL: URL,
    expectedScope: String,
    expectedReviewer: String? = nil,
    relativeTo runURL: URL
) throws -> [E2EReview] {
    let reviewURLs: [URL]
    if let expectedReviewer {
        let reviewURL = directoryURL.appendingPathComponent(
            e2eReviewDirectoryName(reviewer: expectedReviewer)
        )
        guard FileManager.default.fileExists(atPath: reviewURL.path) else {
            return []
        }
        reviewURLs = [reviewURL]
    } else {
        reviewURLs = try e2eReviewDirectoryURLs(in: directoryURL)
    }

    var reviews: [E2EReview] = []
    for reviewURL in reviewURLs {
        let reviewer = String(reviewURL.lastPathComponent.dropFirst("review.".count))
        let urls = try FileManager.default.contentsOfDirectory(
            at: reviewURL,
            includingPropertiesForKeys: [.isRegularFileKey],
            options: [.skipsHiddenFiles]
        )
        .filter { url in
            url.pathExtension.lowercased() == "md"
                && ((try? url.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true)
        }
        .sorted { $0.lastPathComponent < $1.lastPathComponent }

        reviews += urls.compactMap { url in
            try? parseE2EReview(
                at: url,
                expectedScope: expectedScope,
                expectedReviewer: expectedReviewer ?? reviewer,
                relativeTo: runURL
            )
        }
    }
    return reviews.sorted { lhs, rhs in
        if lhs.metadata.reviewer != rhs.metadata.reviewer {
            return lhs.metadata.reviewer < rhs.metadata.reviewer
        }
        return lhs.path < rhs.path
    }
}

func parseE2EReview(
    at url: URL,
    expectedScope: String? = nil,
    expectedReviewer: String? = nil,
    relativeTo runURL: URL
) throws -> E2EReview {
    let text = try String(contentsOf: url, encoding: .utf8)
        .replacingOccurrences(of: "\r\n", with: "\n")
    let parts = try splitE2EReviewFrontMatter(text, url: url)
    let data = Data(parts.json.utf8)
    let metadata = try JSONDecoder().decode(E2EReviewMetadata.self, from: data)

    guard metadata.schema == e2eReviewSchemaID else {
        throw ValidationError("Review has unsupported schema: \(url.path)")
    }
    let title = metadata.title.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !title.isEmpty else {
        throw ValidationError("Review title must not be empty: \(url.path)")
    }
    if let expectedScope, metadata.scope != expectedScope {
        throw ValidationError(
            "Review scope `\(metadata.scope)` does not match expected scope `\(expectedScope)`: \(url.path)"
        )
    }
    if let expectedReviewer, metadata.reviewer != expectedReviewer {
        throw ValidationError(
            "Review reviewer `\(metadata.reviewer)` does not match expected reviewer `\(expectedReviewer)`: \(url.path)"
        )
    }
    let expectedFilename = e2eReviewSlug(title) + ".md"
    guard url.lastPathComponent == expectedFilename else {
        throw ValidationError(
            "Review filename must be the slugged title `\(expectedFilename)`: \(url.path)"
        )
    }

    let markdown = parts.markdown.trimmingCharacters(in: .whitespacesAndNewlines)
    let body = try splitE2EReviewMarkdown(markdown, title: title, url: url)
    return E2EReview(
        metadata: metadata,
        path: e2eReviewRelativePath(from: runURL, to: url),
        title: title,
        summaryMarkdown: body.summary,
        detailsMarkdown: body.details
    )
}

func e2eReviewSlug(_ value: String) -> String {
    let lowercased = value.lowercased()
    var scalars: [UnicodeScalar] = []
    var previousWasDash = false

    for scalar in lowercased.unicodeScalars {
        let isAlphanumeric =
            (scalar.value >= 48 && scalar.value <= 57)
            || (scalar.value >= 97 && scalar.value <= 122)
        if isAlphanumeric {
            scalars.append(scalar)
            previousWasDash = false
        } else if !previousWasDash {
            scalars.append("-")
            previousWasDash = true
        }
    }

    let slug = String(String.UnicodeScalarView(scalars))
        .trimmingCharacters(in: CharacterSet(charactersIn: "-"))
    return slug.isEmpty ? "review" : slug
}

private func e2eReviewDirectoryURLs(in directoryURL: URL) throws -> [URL] {
    guard FileManager.default.fileExists(atPath: directoryURL.path) else {
        return []
    }
    return try FileManager.default.contentsOfDirectory(
        at: directoryURL,
        includingPropertiesForKeys: [.isDirectoryKey],
        options: [.skipsHiddenFiles]
    )
    .filter { url in
        isE2EReviewDirectoryName(url.lastPathComponent)
            && ((try? url.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true)
    }
    .sorted { $0.lastPathComponent < $1.lastPathComponent }
}

private func splitE2EReviewFrontMatter(
    _ text: String,
    url: URL
) throws -> (json: String, markdown: String) {
    guard text.hasPrefix("---\n") else {
        throw ValidationError("Review is missing JSON front matter: \(url.path)")
    }
    let bodyStart = text.index(text.startIndex, offsetBy: 4)
    guard let endRange = text[bodyStart...].range(of: "\n---\n") else {
        throw ValidationError("Review front matter is not closed: \(url.path)")
    }
    let json = String(text[bodyStart..<endRange.lowerBound])
        .trimmingCharacters(in: .whitespacesAndNewlines)
    let markdown = String(text[endRange.upperBound...])
    guard !json.isEmpty else {
        throw ValidationError("Review JSON front matter is empty: \(url.path)")
    }
    return (json, markdown)
}

private func splitE2EReviewMarkdown(
    _ markdown: String,
    title: String,
    url: URL
) throws -> (summary: String, details: String) {
    let h1 = "# \(title)"
    guard markdown == h1 || markdown.hasPrefix(h1 + "\n") else {
        throw ValidationError("Review Markdown must start with `# \(title)`: \(url.path)")
    }
    let afterTitle = markdown.dropFirst(h1.count)
        .trimmingCharacters(in: .whitespacesAndNewlines)
    guard let detailsRange = afterTitle.range(of: "\n## Details\n") else {
        throw ValidationError("Review Markdown must contain `## Details`: \(url.path)")
    }
    let summary = String(afterTitle[..<detailsRange.lowerBound])
        .trimmingCharacters(in: .whitespacesAndNewlines)
    let details = String(afterTitle[detailsRange.upperBound...])
        .trimmingCharacters(in: .whitespacesAndNewlines)
    guard !summary.isEmpty else {
        throw ValidationError("Review summary must not be empty: \(url.path)")
    }
    guard !details.isEmpty else {
        throw ValidationError("Review details must not be empty: \(url.path)")
    }
    return (summary, details)
}

private func e2eReviewRelativePath(from baseURL: URL, to url: URL) -> String {
    let basePath = baseURL.standardizedFileURL.path
    let path = url.standardizedFileURL.path
    let prefix = basePath.hasSuffix("/") ? basePath : basePath + "/"
    guard path.hasPrefix(prefix) else {
        return url.lastPathComponent
    }
    return String(path.dropFirst(prefix.count))
}
