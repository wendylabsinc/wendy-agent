import Hummingbird

// The wire types are generated from `Schemas/Api.schema.json` by the
// swift-json-schema build plugin as plain `Codable, Hashable` structs. These
// conformances let Hummingbird return them directly from route handlers,
// encoding via the request context's JSON encoder. Being internal structs of
// `Sendable` members, they are already implicitly `Sendable`, so they cross
// the actor boundaries between the web server, `AppState`, and `RunStore`
// without further annotation.

extension StateResponse: ResponseEncodable {}
extension PromptUpdateResponse: ResponseEncodable {}
extension RunsResponse: ResponseEncodable {}
extension PersistedRun: ResponseEncodable {}
