import Foundation
import Hummingbird

@main
struct WendyNotify {
    static func main() async throws {
        let history = SendHistory()
        let sender = NotificationSender()
        let csrfToken = UUID().uuidString
        let router = Router()
        router.get("/") { _, _ -> Response in
            let snapshot = await history.snapshot()
            return htmlResponse(renderPage(form: .init(), outcome: nil, records: snapshot.records,
                                           limits: snapshot.limits, csrfToken: csrfToken))
        }
        router.post("/") { request, context -> Response in
            let body = try await request.body.collect(upTo: 128 * 1024)
            let encoded = String(buffer: body)
            // Browsers cannot obtain this token from another origin. Keep sends
            // intentional even when a developer visits another site on the LAN.
            let submittedToken = encoded.split(separator: "&").first { $0.hasPrefix("csrfToken=") }
                .map { String($0.dropFirst("csrfToken=".count)) }
            guard submittedToken == csrfToken else {
                return htmlResponse("<!doctype html><title>Send refused</title><p>Form token is missing or expired. Reload WendyNotify and submit the form again.</p>", status: .forbidden)
            }
            let form = NotificationForm(encoded: encoded)
            let outcome = await sender.send(form)
            if let error = outcome.error {
                context.logger.error("Notification send failed", metadata: [
                    "code": "\(outcome.code)", "message": "\(outcome.message)", "cause": "\(String(reflecting: error))"
                ])
            }
            await history.record(outcome, title: form.title)
            let snapshot = await history.snapshot()
            return htmlResponse(renderPage(form: form, outcome: outcome, records: snapshot.records,
                                           limits: snapshot.limits, csrfToken: csrfToken))
        }
        let app = Application(router: router, configuration: .init(address: .hostname("0.0.0.0", port: 8080)))
        try await app.runService()
    }
}

func htmlResponse(_ html: String, status: HTTPResponse.Status = .ok) -> Response {
    Response(status: status, headers: [.contentType: "text/html; charset=utf-8", .cacheControl: "no-store"],
             body: .init(byteBuffer: .init(string: html)))
}
