import ArgumentParser
import FlyingFox
import FlyingSocks
import Foundation
import GStreamer
import MLX
import MLXHuggingFace
import MLXLMCommon
import Tokenizers
import MLXVLM

/// Minimal OpenAI-compatible server around a local MLX VLM, so HelloVLM's
/// app (or any /v1/chat/completions client) can use MLX as its backend
/// unchanged. Non-streaming, one request at a time — the same serving model
/// as the bundled llama-server (`-np 1`).
@main
struct MLXServer: AsyncParsableCommand {
    static let configuration = CommandConfiguration(commandName: "MLXServer")

    @Option(name: .long, help: "Local path to an MLX VLM model directory.")
    var modelPath: String = "/models/mlx/gemma-3-4b-it-4bit"

    @Option(name: .long, help: "Model id reported by /v1/models (defaults to the directory name).")
    var modelAlias: String?

    @Option(name: .long, help: "Port to listen on.")
    var port: UInt16 = 11434

    @Option(name: .long, help: "Edge length images are decoded to (the model's native size).")
    var imageSize: Int = 896

    mutating func run() async throws {
        let modelDirectory = URL(fileURLWithPath: modelPath, isDirectory: true)
        let alias = modelAlias ?? modelDirectory.lastPathComponent

        print("Loading model: \(modelDirectory.path) …")
        let loadStartedAt = Date()
        let container = try await VLMModelFactory.shared.loadContainer(
            from: modelDirectory, using: #huggingFaceTokenizerLoader())
        print(String(format: "Model loaded in %.1fs", Date().timeIntervalSince(loadStartedAt)))

        let generator = Generator(container: container, imageSize: imageSize)
        let server = HTTPServer(address: try sockaddr_in.inet(ip4: "0.0.0.0", port: port))

        await server.appendRoute("GET /v1/models") { _ in
            try jsonResponse(ModelsResponse(data: [.init(id: alias)]))
        }

        await server.appendRoute("POST /v1/chat/completions") { request in
            do {
                let chat = try JSONDecoder().decode(ChatRequest.self, from: await request.bodyData)
                let completion = try await generator.complete(chat, model: alias)
                return try jsonResponse(completion)
            } catch {
                let message = String(describing: error)
                print("chat/completions failed: \(message)")
                return try jsonResponse(
                    ErrorResponse(error: .init(message: message)), status: .internalServerError)
            }
        }

        print("Serving \(alias) on 0.0.0.0:\(port)")
        try await server.run()
    }
}

/// Serializes inference: one completion at a time, like llama-server -np 1.
actor Generator {
    private let container: ModelContainer
    private let imageSize: Int

    init(container: ModelContainer, imageSize: Int) {
        self.container = container
        self.imageSize = imageSize
    }

    func complete(_ request: ChatRequest, model: String) async throws -> ChatCompletionResponse {
        var texts: [String] = []
        var images: [UserInput.Image] = []
        for message in request.messages where message.role == "user" {
            texts.append(contentsOf: message.texts)
            for jpeg in message.jpegs {
                images.append(.array(try await decodeJPEG(jpeg)))
            }
        }
        let prompt = texts.joined(separator: "\n")

        let userInput = UserInput(chat: [.user(prompt, images: images)])
        let lmInput = try await container.prepare(input: userInput)
        var parameters = GenerateParameters()
        parameters.maxTokens = request.maxTokens ?? 512

        var text = ""
        var info: GenerateCompletionInfo?
        let stream = try await container.generate(input: lmInput, parameters: parameters)
        for await generation in stream {
            switch generation {
            case .chunk(let chunk):
                text += chunk
            case .info(let completionInfo):
                info = completionInfo
            default:
                break
            }
        }

        var timings: ChatCompletionResponse.Timings?
        if let info {
            timings = .init(
                promptN: info.promptTokenCount,
                promptMs: info.promptTime * 1000,
                predictedN: info.generationTokenCount,
                predictedMs: info.generateTime * 1000,
                predictedPerSecond: info.tokensPerSecond
            )
        }

        return ChatCompletionResponse(
            id: "chatcmpl-\(UUID().uuidString)",
            model: model,
            choices: [.init(index: 0, message: .init(role: "assistant", content: text), finishReason: "stop")],
            timings: timings
        )
    }

    /// Decodes a JPEG into an `[H, W, 3]` RGB MLXArray at the model's native
    /// size via GStreamer, so the MLX-side resize is a no-op. Same pipeline
    /// as MLXVisionProbe's --image-file path.
    private func decodeJPEG(_ jpeg: Data) async throws -> MLXArray {
        let file = FileManager.default.temporaryDirectory
            .appendingPathComponent("mlxserver-\(UUID().uuidString).jpg")
        try jpeg.write(to: file)
        defer { try? FileManager.default.removeItem(at: file) }

        let pipeline = try Pipeline(
            """
            filesrc location=\(file.path) ! jpegdec ! \
            videoconvert ! videoscale ! \
            video/x-raw,format=RGB,width=\(imageSize),height=\(imageSize) ! \
            appsink name=sink
            """)
        let sink = try AppSink(pipeline: pipeline, name: "sink")
        try pipeline.play()
        defer { pipeline.stop() }

        for try await frame in sink.frames() {
            let expected = frame.width * frame.height * 3
            return try frame.withUnsafeBytes { buffer -> MLXArray in
                guard buffer.count == expected else {
                    throw ValidationError(
                        "unexpected buffer size \(buffer.count), expected \(expected)")
                }
                return MLXArray([UInt8](buffer), [frame.height, frame.width, 3])
            }
        }
        throw ValidationError("JPEG decode produced no frame")
    }
}

// MARK: - OpenAI wire types (the subset HelloVLM's VLMClient uses)

struct ChatRequest: Decodable {
    struct Message: Decodable {
        struct ContentPart: Decodable {
            struct ImageURL: Decodable {
                let url: String
            }
            let type: String
            let text: String?
            let imageURL: ImageURL?

            enum CodingKeys: String, CodingKey {
                case type, text
                case imageURL = "image_url"
            }
        }

        let role: String
        let parts: [ContentPart]
        let plainText: String?

        enum CodingKeys: String, CodingKey {
            case role, content
        }

        // Swift.Decoder: ArgumentParser exports a `Decoder` that shadows it.
        init(from decoder: any Swift.Decoder) throws {
            let outer = try decoder.container(keyedBy: CodingKeys.self)
            role = try outer.decode(String.self, forKey: .role)
            if let string = try? outer.decode(String.self, forKey: .content) {
                plainText = string
                parts = []
            } else {
                plainText = nil
                parts = try outer.decodeIfPresent([ContentPart].self, forKey: .content) ?? []
            }
        }

        var texts: [String] {
            if let plainText { return [plainText] }
            return parts.compactMap { $0.type == "text" ? $0.text : nil }
        }

        var jpegs: [Data] {
            parts.compactMap { part in
                guard part.type == "image_url", let url = part.imageURL?.url,
                    let range = url.range(of: "base64,")
                else { return nil }
                return Data(base64Encoded: String(url[range.upperBound...]))
            }
        }
    }

    let messages: [Message]
    let maxTokens: Int?

    enum CodingKeys: String, CodingKey {
        case messages
        case maxTokens = "max_tokens"
    }
}

struct ModelsResponse: Encodable {
    struct Model: Encodable {
        let id: String
        var object = "model"
    }
    var object = "list"
    let data: [Model]
}

struct ChatCompletionResponse: Encodable {
    struct Choice: Encodable {
        struct Message: Encodable {
            let role: String
            let content: String
        }
        let index: Int
        let message: Message
        let finishReason: String

        enum CodingKeys: String, CodingKey {
            case index, message
            case finishReason = "finish_reason"
        }
    }

    struct Timings: Encodable {
        let promptN: Int
        let promptMs: Double
        let predictedN: Int
        let predictedMs: Double
        let predictedPerSecond: Double

        enum CodingKeys: String, CodingKey {
            case promptN = "prompt_n"
            case promptMs = "prompt_ms"
            case predictedN = "predicted_n"
            case predictedMs = "predicted_ms"
            case predictedPerSecond = "predicted_per_second"
        }
    }

    let id: String
    var object = "chat.completion"
    let model: String
    let choices: [Choice]
    let timings: Timings?
}

struct ErrorResponse: Encodable {
    struct ErrorBody: Encodable {
        let message: String
    }
    let error: ErrorBody
}

private func jsonResponse(_ value: some Encodable, status: HTTPStatusCode = .ok) throws -> HTTPResponse {
    HTTPResponse(
        statusCode: status,
        headers: [.contentType: "application/json"],
        body: try JSONEncoder().encode(value)
    )
}
