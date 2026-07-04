import Foundation

#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

/// Minimal OpenAI-compatible chat-completions client.
///
/// Works against the bundled llama-server backend as well as any other
/// endpoint that speaks `/v1/models` and `/v1/chat/completions`
/// (Ollama, vLLM, llama.cpp, ...).
struct VLMClient {
    let baseURL: URL
    let configuredModel: String?

    private let session: URLSession

    init(baseURL: URL, model: String?) {
        self.baseURL = baseURL
        self.configuredModel = model?.isEmpty == true ? nil : model

        let configuration = URLSessionConfiguration.default
        configuration.timeoutIntervalForRequest = 600
        configuration.timeoutIntervalForResource = 600
        self.session = URLSession(configuration: configuration)
    }

    struct ChatResult {
        let text: String
        let modelName: String
        let stats: String?
    }

    enum ClientError: LocalizedError {
        case badStatus(Int, String)
        case emptyResponse

        var errorDescription: String? {
            switch self {
            case .badStatus(let code, let body):
                return "VLM backend returned HTTP \(code): \(body.prefix(200))"
            case .emptyResponse:
                return "VLM backend returned an empty response."
            }
        }
    }

    /// Polls `/v1/models` until the backend is up and a model is loaded.
    /// The bundled backend may still be downloading the model on first
    /// start, so this can legitimately take minutes.
    func waitForModel(pollInterval: Duration = .seconds(3), onAttempt: (Int) async -> Void) async throws -> (name: String, backend: String?) {
        var attempt = 0
        while !Task.isCancelled {
            attempt += 1
            await onAttempt(attempt)
            if let model = try? await firstModel() {
                return model
            }
            try await Task.sleep(for: pollInterval)
        }
        throw CancellationError()
    }

    private func firstModel() async throws -> (name: String, backend: String?) {
        struct ModelsResponse: Decodable {
            struct Model: Decodable {
                let id: String
                let ownedBy: String?

                enum CodingKeys: String, CodingKey {
                    case id
                    case ownedBy = "owned_by"
                }
            }
            let data: [Model]
        }

        let url = baseURL.appendingPathComponent("v1/models")
        let (data, response) = try await send(URLRequest(url: url))
        guard response.statusCode == 200 else {
            throw ClientError.badStatus(response.statusCode, String(decoding: data, as: UTF8.self))
        }
        let models = try AppJSON.decoder.decode(ModelsResponse.self, from: data)
        guard let first = models.data.first else {
            throw ClientError.emptyResponse
        }
        return (configuredModel ?? first.id, Self.backendDisplayName(first.ownedBy))
    }

    /// Maps the OpenAI `owned_by` field to a display name for the engine.
    /// llama-server reports "llamacpp"; MLXServer reports "mlx".
    private static func backendDisplayName(_ ownedBy: String?) -> String? {
        switch ownedBy?.lowercased() {
        case nil, "": return nil
        case "llamacpp": return "llama.cpp"
        case "mlx": return "MLX"
        case "ollama": return "Ollama"
        case "openai", "organization-owner": return nil
        case .some(let other): return other
        }
    }

    func chat(prompt: String, jpegFrames: [Data], model: String) async throws -> ChatResult {
        struct ContentPart: Encodable {
            struct ImageURL: Encodable {
                let url: String
            }
            let type: String
            var text: String?
            var imageURL: ImageURL?

            enum CodingKeys: String, CodingKey {
                case type
                case text
                case imageURL = "image_url"
            }
        }
        struct Message: Encodable {
            let role: String
            let content: [ContentPart]
        }
        struct Request: Encodable {
            let model: String
            let messages: [Message]
            let maxTokens: Int
            let stream: Bool

            enum CodingKeys: String, CodingKey {
                case model
                case messages
                case maxTokens = "max_tokens"
                case stream
            }
        }
        struct Response: Decodable {
            struct Choice: Decodable {
                struct Message: Decodable {
                    let content: String?
                }
                let message: Message
            }
            struct Timings: Decodable {
                let promptN: Int?
                let promptMs: Double?
                let predictedN: Int?
                let predictedPerSecond: Double?

                enum CodingKeys: String, CodingKey {
                    case promptN = "prompt_n"
                    case promptMs = "prompt_ms"
                    case predictedN = "predicted_n"
                    case predictedPerSecond = "predicted_per_second"
                }
            }
            let model: String?
            let choices: [Choice]
            let timings: Timings?
        }

        var parts: [ContentPart] = [ContentPart(type: "text", text: prompt)]
        for jpeg in jpegFrames {
            let dataURL = "data:image/jpeg;base64,\(jpeg.base64EncodedString())"
            parts.append(ContentPart(type: "image_url", imageURL: .init(url: dataURL)))
        }

        let body = Request(
            model: model,
            messages: [Message(role: "user", content: parts)],
            maxTokens: 512,
            stream: false
        )

        var request = URLRequest(url: baseURL.appendingPathComponent("v1/chat/completions"))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONEncoder().encode(body)

        let (data, response) = try await send(request)
        guard response.statusCode == 200 else {
            throw ClientError.badStatus(response.statusCode, String(decoding: data, as: UTF8.self))
        }

        let decoded = try JSONDecoder().decode(Response.self, from: data)
        guard let text = decoded.choices.first?.message.content, !text.isEmpty else {
            throw ClientError.emptyResponse
        }

        var stats: String?
        if let timings = decoded.timings,
           let promptN = timings.promptN,
           let promptMs = timings.promptMs,
           let predictedN = timings.predictedN,
           let predictedPerSecond = timings.predictedPerSecond {
            stats = String(
                format: "%d prompt tokens · prefill %.1fs · %d generated · %.1f tok/s",
                promptN, promptMs / 1000, predictedN, predictedPerSecond
            )
        }

        return ChatResult(text: text, modelName: decoded.model ?? model, stats: stats)
    }

    /// async URLSession wrapper that also works with FoundationNetworking on Linux.
    private func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        try await withCheckedThrowingContinuation { continuation in
            let task = session.dataTask(with: request) { data, response, error in
                if let error {
                    continuation.resume(throwing: error)
                    return
                }
                guard let data, let http = response as? HTTPURLResponse else {
                    continuation.resume(throwing: URLError(.badServerResponse))
                    return
                }
                continuation.resume(returning: (data, http))
            }
            task.resume()
        }
    }
}
