import Hummingbird
import AsyncHTTPClient

struct OllamaResponse: Codable {
    let model: String
    let created_at: String
    let response: String
    let done: Bool
    let context: [Int]
    let total_duration: Int
    let load_duration: Int
    let prompt_eval_count: Int
    let prompt_eval_duration: Int
    let eval_count: Int
    let eval_duration: Int
}

struct OllamaRequest: ResponseCodable {
    let model: String
    let prompt: String
    let stream: Bool
}

// create router and add a single GET /hello route
let router = Router()
router.get("/") { request, _ -> OllamaResponse in
let query = request.uri.queryParameters.get("query") ?? "How do I make lasagna?"
    // create http client
    let client = HTTPClient.shared
    
    var request = HTTPClientRequest(
        url: "http://host.docker.internal:42033/api/generate"
    )
    request.method = .POST
    request.headers.add(name: "Content-Type", value: "application/json")
    request.body = try JSONEncoder().encode(OllamaRequest(model: "tinyllama", prompt: query, stream: false))
    let response = try await client.execute(request, timeout: .seconds(30))
    let body = try await response.body.collect(upTo: 1024 * 1024) 
    let ollamaResponse = try JSONDecoder().decode(OllamaResponse.self, from: body)
    return ollamaResponse
}
// create application using router
let app = Application(
    router: router,
    configuration: .init(address: .hostname("0.0.0.0", port: 8080))
)
// run hummingbird application
try await app.runService()
