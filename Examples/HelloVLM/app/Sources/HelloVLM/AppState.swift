import Foundation

// The wire types (`StateResponse`, `CameraStatus`, `PromptUpdateRequest`, …)
// are generated from `Schemas/Api.schema.json` by the swift-json-schema build
// plugin. Because JSON Schema has no date type, their timestamp fields are
// ISO-8601 `String`s; this actor keeps `Date` internally and converts at the
// snapshot boundary.

actor AppState {
    private let startedAt = Date()
    private let baseURL: String
    private let interval: Double
    private let fps: Double
    private let resolution: Int

    private var cameraStatus: CameraStatus = .starting
    private var cameraName: String?
    private var lastFrameAt: Date?
    private var lastFrameJPEG: Data?

    private var modelStatus: ModelStatus = .loading
    private var modelName: String?
    private var modelBackend: String?

    private var promptText: String
    private var promptUpdatedAt = Date()

    private var isRunningInference = false
    private var latestRunID: String?
    private var latestRunDuration: TimeInterval?
    private var lastInferenceAt: Date?
    private var lastError: String?

    init(config: AppConfig, baseURL: String, latestRun: PersistedRun?) {
        self.baseURL = baseURL
        self.interval = config.interval
        self.fps = config.fps
        self.resolution = config.height
        self.modelName = config.model
        self.promptText = config.prompt
        self.latestRunID = latestRun?.id
        self.latestRunDuration = latestRun?.duration
        self.lastInferenceAt = latestRun.flatMap { ISO8601.date(from: $0.timestamp) }
    }

    func snapshot() -> StateResponse {
        StateResponse(
            app: AppInfo(startedAt: ISO8601.dateString(from: startedAt), url: baseURL),
            camera: CameraInfo(
                frameURL: lastFrameAt.map {
                    let encoded = ISO8601.dateString(from: $0)
                    return "/frame.jpg?t=\(encoded.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? encoded)"
                },
                lastFrameAt: lastFrameAt.map { ISO8601.dateString(from: $0) },
                name: cameraName,
                status: cameraStatus
            ),
            error: lastError,
            model: ModelInfo(backend: modelBackend, name: modelName, status: modelStatus),
            prompt: PromptInfo(text: promptText, updatedAt: ISO8601.dateString(from: promptUpdatedAt)),
            run: RunInfo(
                fps: fps,
                interval: interval,
                isRunningInference: isRunningInference,
                lastInferenceAt: lastInferenceAt.map { ISO8601.dateString(from: $0) },
                latestRunDuration: latestRunDuration,
                latestRunID: latestRunID,
                resolution: resolution
            )
        )
    }

    func liveFrameJPEG() -> Data? {
        lastFrameJPEG
    }

    func currentPrompt() -> String {
        promptText
    }

    func savePrompt(_ text: String) -> PromptUpdateResponse {
        promptText = text
        promptUpdatedAt = Date()
        return PromptUpdateResponse(
            ok: true,
            prompt: PromptInfo(text: promptText, updatedAt: ISO8601.dateString(from: promptUpdatedAt))
        )
    }

    func setCameraStarting() {
        cameraStatus = .starting
        lastError = nil
    }

    func setCameraReady(name: String) {
        cameraStatus = .ready
        cameraName = name
        if lastError == "No camera found." {
            lastError = nil
        }
    }

    func setCameraFailed(message: String) {
        cameraStatus = .failed
        lastError = message
    }

    func setLiveFrame(jpeg: Data, at date: Date) {
        lastFrameJPEG = jpeg
        lastFrameAt = date
    }

    func setModelLoading(name: String?) {
        modelStatus = .loading
        modelName = name
        modelBackend = nil
        lastError = nil
    }

    func setModelReady(name: String?, backend: String?) {
        modelStatus = .ready
        modelName = name
        modelBackend = backend
    }

    func setModelFailed(message: String, name: String?) {
        modelStatus = .failed
        modelName = name
        modelBackend = nil
        lastError = message
    }

    func setInferenceRunning(_ isRunning: Bool) {
        isRunningInference = isRunning
    }

    func recordRun(id: String, at date: Date, duration: TimeInterval) {
        latestRunID = id
        latestRunDuration = duration
        lastInferenceAt = date
    }

    func setError(_ message: String?) {
        lastError = message
    }
}
