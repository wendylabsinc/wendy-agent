import ArgumentParser
import Foundation
import Hummingbird

struct AppConfig: Encodable {
    var llmURL: String
    var model: String?
    var prompt: String
    var camera: String?
    var interval: Double
    var fps: Double
    var width: Int
    var height: Int
    var maxFrames: Int
    var port: Int
}

private func printFinalConfig(_ config: AppConfig) {
    do {
        let data = try AppJSON.encoder.encode(config)
        if let json = String(data: data, encoding: .utf8) {
            print("Final app config:")
            print(json)
            return
        }
    } catch {
        print("Failed to encode final app config: \(error)")
    }
}

struct CLIArguments: ParsableCommand {
    static let configuration = CommandConfiguration(commandName: "HelloVLM")

    @Option(name: .long, help: "Base URL of the OpenAI-compatible VLM backend.")
    var llmUrl: String = "http://localhost:11434"

    @Option(name: .long, help: "Model name to request. Defaults to the first model the backend reports.")
    var model: String?

    @Option(name: .long, help: "Prompt to run against the model and the current camera frames.")
    var prompt: String =
        "These frames were captured at a fixed interval from a webcam. Reply in English. You are a comedian using dark humor below the waist line. Ridicule what is shown by the frames."

    @Option(name: .long, help: "Name of the camera to use (substring match).")
    var camera: String?

    @Option(name: .long, help: "Seconds of camera history to include in each inference pass.")
    var interval: Double = 5

    @Option(name: .long, help: "Frames per second to sample into the buffer.")
    var fps: Double = 0.5

    @Option(name: .long, help: "Camera frame width.")
    var width: Int = 640

    @Option(name: .long, help: "Camera frame height.")
    var height: Int = 480

    @Option(name: .long, help: "Maximum number of frames to send per inference pass.")
    var maxFrames: Int = 4

    @Option(name: .long, help: "Local port to serve the web UI on.")
    var port: Int = 8080

    func validate() throws {
        guard (1...65535).contains(port) else {
            throw ValidationError("--port must be between 1 and 65535.")
        }
        guard interval > 0 else {
            throw ValidationError("--interval must be greater than 0.")
        }
        guard fps > 0 else {
            throw ValidationError("--fps must be greater than 0.")
        }
        guard width > 0, height > 0 else {
            throw ValidationError("--width and --height must be greater than 0.")
        }
        guard maxFrames > 0 else {
            throw ValidationError("--max-frames must be greater than 0.")
        }
        guard URL(string: llmUrl) != nil else {
            throw ValidationError("--llm-url must be a valid URL.")
        }
    }
}

let appConfig: AppConfig = {
    do {
        let parsed = try CLIArguments.parse(Array(CommandLine.arguments.dropFirst()))
        let config = AppConfig(
            llmURL: parsed.llmUrl,
            model: parsed.model,
            prompt: parsed.prompt,
            camera: parsed.camera,
            interval: parsed.interval,
            fps: parsed.fps,
            width: parsed.width,
            height: parsed.height,
            maxFrames: parsed.maxFrames,
            port: parsed.port
        )
        printFinalConfig(config)
        return config
    } catch {
        CLIArguments.exit(withError: error)
    }
}()

@main
struct HelloVLM {
    static func main() async {
        do {
            let dataDirectory = try AppDirectories.makeDataDirectory()
            let runStore = try RunStore(rootURL: dataDirectory)
            let baseURL = makeAdvertisedBaseURL(port: appConfig.port)
            let state = AppState(config: appConfig, baseURL: baseURL, latestRun: runStore.latestRun())
            let indexHTML = try loadIndexHTML()

            let app = buildWebApplication(
                port: appConfig.port,
                state: state,
                runStore: runStore,
                indexHTML: indexHTML,
                onServerRunning: { _ in
                    print("HELLO_VLM_URL=\(baseURL)")
                    print("HELLO_VLM_DATA_DIR=\(dataDirectory.path)")
                }
            )

            let frameBuffer = FrameBuffer()
            let camera = LinuxCamera(config: appConfig, state: state, buffer: frameBuffer)
            await camera.start()

            let inferenceTask = Task {
                await runInferenceLoop(state: state, runStore: runStore, frameBuffer: frameBuffer)
            }

            defer {
                inferenceTask.cancel()
            }

            try await app.runService()
        } catch {
            print("HelloVLM failed to start: \(error)")
            exit(1)
        }
    }

    /// Polls the backend until a model is served, updating the UI state on
    /// the way; returns the model name + backend engine. Blocks until the
    /// backend answers; throws only on task cancellation.
    static func discoverModel(client: VLMClient, state: AppState) async throws -> (name: String, backend: String?) {
        await state.setModelLoading(name: appConfig.model)
        print("Waiting for VLM backend at \(appConfig.llmURL) …")
        let model = try await client.waitForModel { attempt in
            if attempt % 10 == 1 {
                print("  Waiting for VLM backend (attempt \(attempt))…")
            }
        }
        print("VLM backend ready, model: \(model.name)\(model.backend.map { " (\($0))" } ?? "")")
        await state.setModelReady(name: model.name, backend: model.backend)
        return model
    }

    static func runInferenceLoop(state: AppState, runStore: RunStore, frameBuffer: FrameBuffer) async {
        guard let baseURL = URL(string: appConfig.llmURL) else { return }
        let client = VLMClient(baseURL: baseURL, model: appConfig.model)
        let deviceName = ProcessInfo.processInfo.environment["WENDY_DEVICE_HOSTNAME"]
            ?? ProcessInfo.processInfo.hostName

        var modelName: String
        var modelBackend: String?
        do {
            (modelName, modelBackend) = try await discoverModel(client: client, state: state)
        } catch {
            return  // waitForModel only throws on task cancellation
        }

        print("Sampling at \(appConfig.fps) fps, evaluating last \(appConfig.interval)s of frames (max \(appConfig.maxFrames)).")

        while !Task.isCancelled {
            let prompt = await state.currentPrompt().trimmingCharacters(in: .whitespacesAndNewlines)
            guard !prompt.isEmpty else {
                try? await Task.sleep(for: .seconds(1))
                continue
            }

            while !Task.isCancelled && frameBuffer.window(within: appConfig.interval).isEmpty {
                try? await Task.sleep(for: .seconds(1))
            }
            if Task.isCancelled { return }

            let window = frameBuffer.window(within: appConfig.interval)
            let frames = Array(window.suffix(appConfig.maxFrames))
            guard !frames.isEmpty else { continue }

            print("Prompt: \(prompt)")
            print("Running inference on \(frames.count) frame(s)…")
            await state.setInferenceRunning(true)

            let inferenceStartedAt = Date()
            var result: VLMClient.ChatResult?
            do {
                result = try await client.chat(
                    prompt: prompt,
                    jpegFrames: frames.map(\.jpeg),
                    model: modelName
                )
            } catch {
                print("Generation failed: \(error)")
                await state.setError("Generation failed: \(error.localizedDescription)")
                // The backend may have been replaced under us (llm/ and
                // llm-mlx/ deploy into the same service slot). Re-discover so
                // the model label, requested name, and per-run provenance
                // stay truthful across swaps.
                if let fresh = try? await discoverModel(client: client, state: state) {
                    (modelName, modelBackend) = fresh
                }
            }
            await state.setInferenceRunning(false)

            guard let result else { continue }
            let duration = Date().timeIntervalSince(inferenceStartedAt)
            let cleanedResponse = result.text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !cleanedResponse.isEmpty else { continue }

            print("Response: \(cleanedResponse.prefix(160))\(cleanedResponse.count > 160 ? "…" : "")")
            if let stats = result.stats {
                print("Stats: \(stats)")
            }

            do {
                let cameraName = await state.snapshot().camera.name
                let run = try runStore.persistRun(
                    prompt: prompt,
                    response: cleanedResponse,
                    frames: frames,
                    cameraName: cameraName,
                    modelName: result.modelName,
                    backend: modelBackend,
                    device: deviceName,
                    interval: appConfig.interval,
                    fps: appConfig.fps,
                    resolution: appConfig.height,
                    duration: duration,
                    stats: result.stats
                )
                await state.recordRun(id: run.id, at: ISO8601.date(from: run.timestamp) ?? Date(), duration: duration)
                await state.setError(nil)
            } catch {
                await state.setError("Failed to persist run: \(error.localizedDescription)")
            }
        }
    }
}

enum AppDirectories {
    static func makeDataDirectory() throws -> URL {
        let url = URL(fileURLWithPath: FileManager.default.currentDirectoryPath, isDirectory: true)
            .appendingPathComponent("Runs", isDirectory: true)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }
}

enum AppJSON {
    static let encoder: JSONEncoder = {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        encoder.dateEncodingStrategy = .custom { date, encoder in
            var container = encoder.singleValueContainer()
            try container.encode(ISO8601.dateString(from: date))
        }
        return encoder
    }()

    static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let value = try container.decode(String.self)
            guard let date = ISO8601.date(from: value) else {
                throw DecodingError.dataCorruptedError(in: container, debugDescription: "Invalid ISO-8601 date: \(value)")
            }
            return date
        }
        return decoder
    }()
}

enum ISO8601 {
    // ISO8601DateFormatter is documented thread-safe.
    nonisolated(unsafe) private static let formatter: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        return formatter
    }()

    static func dateString(from date: Date) -> String {
        formatter.string(from: date)
    }

    static func date(from string: String) -> Date? {
        formatter.date(from: string)
    }
}

enum RunID {
    private static let formatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.calendar = Calendar(identifier: .iso8601)
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.dateFormat = "yyyy-MM-dd'T'HH-mm-ss.SSS'Z'"
        return formatter
    }()

    static func make(date: Date = Date()) -> String {
        formatter.string(from: date)
    }
}

func makeAdvertisedBaseURL(port: Int) -> String {
    // Inside a wendy container the machine hostname is the container's, not
    // the device's — prefer the device hostname the agent injects.
    let hostname = (ProcessInfo.processInfo.environment["WENDY_DEVICE_HOSTNAME"]
        ?? ProcessInfo.processInfo.hostName)
        .trimmingCharacters(in: .whitespacesAndNewlines)
    let host = hostname.isEmpty ? "localhost" : hostname
    return "http://\(host):\(port)/"
}

func loadIndexHTML() throws -> String {
    if let bundled = Bundle.module.url(forResource: "Resources/index", withExtension: "html")
        ?? Bundle.module.url(forResource: "index", withExtension: "html") {
        return try String(contentsOf: bundled, encoding: .utf8)
    }

    // Fallback for running the binary outside its resource bundle.
    let fileManager = FileManager.default
    let sourceDirectory = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
    let workingDirectory = URL(fileURLWithPath: fileManager.currentDirectoryPath)
    let candidates = [
        sourceDirectory.appendingPathComponent("Resources/index.html"),
        workingDirectory.appendingPathComponent("Resources/index.html"),
        workingDirectory.appendingPathComponent("index.html")
    ]

    for candidate in candidates where fileManager.fileExists(atPath: candidate.path) {
        return try String(contentsOf: candidate, encoding: .utf8)
    }

    let searchedPaths = candidates.map(\.path).joined(separator: ", ")
    throw NSError(
        domain: "HelloVLM",
        code: 1,
        userInfo: [NSLocalizedDescriptionKey: "Could not locate index.html. Searched Bundle.module and: \(searchedPaths)"]
    )
}
