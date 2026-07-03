import Foundation

// `PersistedFrame`, `PersistedRun`, and `RunsResponse` are generated from
// `Schemas/Api.schema.json` by the swift-json-schema build plugin. Their
// timestamp fields are ISO-8601 `String`s (JSON Schema has no date type), so
// this store formats/parses at the boundary.

struct FrameCapture: Sendable {
    let capturedAt: Date
    let jpeg: Data
}

struct RunStore: Sendable {
    let rootURL: URL
    private let runsURL: URL

    private var fileManager: FileManager { .default }
    private var decoder: JSONDecoder { AppJSON.decoder }
    private var encoder: JSONEncoder { AppJSON.encoder }

    init(rootURL: URL) throws {
        self.rootURL = rootURL
        self.runsURL = rootURL.appendingPathComponent("runs", isDirectory: true)
        try fileManager.createDirectory(at: runsURL, withIntermediateDirectories: true)
    }

    func latestRun() -> PersistedRun? {
        try? listRuns(limit: 1, before: nil).items.first
    }

    func listRuns(limit: Int, before cursor: String?) throws -> RunsResponse {
        let runs = try allRuns()
        let startIndex: Int
        if let cursor, let index = runs.firstIndex(where: { $0.id == cursor }) {
            startIndex = index + 1
        } else {
            startIndex = 0
        }

        let page = Array(runs.dropFirst(startIndex).prefix(limit))
        let nextCursor = startIndex + page.count < runs.count ? page.last?.id : nil
        return RunsResponse(items: page, nextCursor: nextCursor)
    }

    func loadRun(id: String) throws -> PersistedRun {
        let url = runsURL.appendingPathComponent(id, isDirectory: true).appendingPathComponent("result.json")
        let data = try Data(contentsOf: url)
        return try decoder.decode(PersistedRun.self, from: data)
    }

    func persistRun(
        prompt: String,
        response: String,
        frames: [FrameCapture],
        cameraName: String?,
        modelName: String?,
        backend: String?,
        device: String?,
        interval: Double,
        fps: Double,
        resolution: Int,
        duration: TimeInterval,
        stats: String?
    ) throws -> PersistedRun {
        let id = RunID.make()
        let directoryURL = runsURL.appendingPathComponent(id, isDirectory: true)
        try fileManager.createDirectory(at: directoryURL, withIntermediateDirectories: true)

        var persistedFrames: [PersistedFrame] = []
        persistedFrames.reserveCapacity(frames.count)

        for (index, frame) in frames.enumerated() {
            let filename = String(format: "frame-%03d.jpg", index)
            let fileURL = directoryURL.appendingPathComponent(filename)
            try frame.jpeg.write(to: fileURL, options: .atomic)
            persistedFrames.append(
                PersistedFrame(
                    capturedAt: ISO8601.dateString(from: frame.capturedAt),
                    url: "/artifacts/runs/\(id)/\(filename)"
                )
            )
        }

        let run = PersistedRun(
            backend: backend,
            cameraName: cameraName,
            device: device,
            duration: duration,
            fps: fps,
            frameCount: persistedFrames.count,
            frames: persistedFrames,
            id: id,
            interval: interval,
            modelName: modelName,
            prompt: prompt,
            resolution: resolution,
            response: response,
            stats: stats,
            timestamp: ISO8601.dateString(from: Date())
        )

        let temporaryURL = directoryURL.appendingPathComponent("result.json.tmp")
        let finalURL = directoryURL.appendingPathComponent("result.json")
        let data = try encoder.encode(run)
        try data.write(to: temporaryURL, options: .atomic)
        if fileManager.fileExists(atPath: finalURL.path) {
            try fileManager.removeItem(at: finalURL)
        }
        try fileManager.moveItem(at: temporaryURL, to: finalURL)
        return run
    }

    private func allRuns() throws -> [PersistedRun] {
        let directories = try fileManager.contentsOfDirectory(
            at: runsURL,
            includingPropertiesForKeys: nil,
            options: [.skipsHiddenFiles]
        )

        return try directories.compactMap { directoryURL in
            let resultURL = directoryURL.appendingPathComponent("result.json")
            guard fileManager.fileExists(atPath: resultURL.path) else { return nil }
            let data = try Data(contentsOf: resultURL)
            return try decoder.decode(PersistedRun.self, from: data)
        }
        .sorted { $0.timestamp > $1.timestamp }
    }
}
