import Foundation
import Hummingbird
import NIOCore

/// Builds the Hummingbird application serving the web UI and JSON API.
///
/// The wire types returned/decoded here (`StateResponse`, `RunsResponse`,
/// `PersistedRun`, `PromptUpdateRequest`, `PromptUpdateResponse`, …) are
/// generated at build time by the swift-json-schema plugin from
/// `Schemas/Api.schema.json`; see `WireTypes.swift` for the Hummingbird
/// conformances layered on top.
func buildWebApplication(
    port: Int,
    state: AppState,
    runStore: RunStore,
    indexHTML: String,
    onServerRunning: @escaping @Sendable (any Channel) async -> Void
) -> some ApplicationProtocol {
    let router = Router()

    // Preserve the previous server's no-store semantics for every response.
    router.add(middleware: NoStoreMiddleware())

    // Serve archived run frames from the data directory under /artifacts/*.
    router.add(middleware: FileMiddleware(runStore.rootURL.path, urlBasePath: "/artifacts"))

    router.get("/") { _, _ -> Response in
        htmlResponse(indexHTML)
    }

    router.get("/api/state") { _, _ -> StateResponse in
        await state.snapshot()
    }

    router.post("/api/prompt") { request, context -> PromptUpdateResponse in
        let update = try await request.decode(as: PromptUpdateRequest.self, context: context)
        return await state.savePrompt(update.text)
    }

    router.get("/api/runs") { request, _ -> RunsResponse in
        let limit = max(1, min(request.uri.queryParameters.get("limit").flatMap { Int(String($0)) } ?? 4, 50))
        let before = request.uri.queryParameters.get("before").map { String($0) }
        return try runStore.listRuns(limit: limit, before: before)
    }

    router.get("/api/runs/{id}") { _, context -> PersistedRun in
        let id = try context.parameters.require("id")
        do {
            return try runStore.loadRun(id: id)
        } catch {
            throw HTTPError(.notFound, message: "Run not found.")
        }
    }

    router.get("/frame.jpg") { _, _ -> Response in
        guard let data = await state.liveFrameJPEG() else {
            return textResponse("Live frame not available yet.", status: .notFound)
        }
        return dataResponse(data, contentType: "image/jpeg")
    }

    return Application(
        router: router,
        configuration: .init(address: .hostname("0.0.0.0", port: port)),
        onServerRunning: onServerRunning
    )
}

/// Adds `Cache-Control: no-store` to any response that does not already set it.
private struct NoStoreMiddleware<Context: RequestContext>: RouterMiddleware {
    func handle(
        _ request: Request,
        context: Context,
        next: (Request, Context) async throws -> Response
    ) async throws -> Response {
        var response = try await next(request, context)
        if response.headers[.cacheControl] == nil {
            response.headers[.cacheControl] = "no-store"
        }
        return response
    }
}

private func htmlResponse(_ html: String) -> Response {
    dataResponse(Data(html.utf8), contentType: "text/html; charset=utf-8")
}

private func textResponse(_ text: String, status: HTTPResponse.Status = .ok) -> Response {
    dataResponse(Data(text.utf8), contentType: "text/plain; charset=utf-8", status: status)
}

private func dataResponse(
    _ data: Data,
    contentType: String,
    status: HTTPResponse.Status = .ok
) -> Response {
    var buffer = ByteBuffer()
    buffer.writeBytes(data)
    return Response(
        status: status,
        headers: [.contentType: contentType],
        body: .init(byteBuffer: buffer)
    )
}
