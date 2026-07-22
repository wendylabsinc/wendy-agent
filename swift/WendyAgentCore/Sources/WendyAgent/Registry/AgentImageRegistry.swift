import Crypto
import Foundation
import HTTPTypes
import Hummingbird
import Logging
import NIOCore

/// Minimal OCI Distribution v2 server backed by `BlobStore`. Handles the subset
/// docker/container use to push and pull: version check, blob uploads
/// (monolithic + chunked), blob read, manifest put/get.
///
/// Repository names are treated as a single path segment (`{repo}`); nested
/// repository names (e.g. `library/app`) are out of scope for this minimal
/// server. The behavior contract is what matters here — routes, statuses,
/// digest verification, and `Location` headers — verified end-to-end in
/// Task 9.
struct AgentImageRegistry: Sendable {
    /// Maximum accepted blob size (container image layers can be several GiB).
    private static let maxBlobSize = 8 * 1024 * 1024 * 1024
    /// Maximum accepted manifest size (JSON, always small).
    private static let maxManifestSize = 16 * 1024 * 1024

    private let store: BlobStore
    private let port: Int
    private let logger = Logger(label: "sh.wendy.agent.registry")
    private let uploads = UploadBuffers()

    /// `Docker-Content-Digest` — clients (notably Apple `container`) require this
    /// header on blob and manifest GET/HEAD responses to resolve a tag/reference
    /// to its content digest. The literal is a valid HTTP field token.
    private static let dockerContentDigest = HTTPField.Name("Docker-Content-Digest")!

    /// The canonical `sha256:<hex>` content digest of `data`.
    private static func contentDigest(of data: Data) -> String {
        let hex = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        return "sha256:\(hex)"
    }

    /// The manifest's own `mediaType`, defaulting to the OCI image manifest type.
    /// Clients (Apple `container`) require `Content-Type` on both GET *and* HEAD
    /// manifest responses.
    private static func manifestMediaType(_ data: Data) -> String {
        (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["mediaType"]
            as? String ?? "application/vnd.oci.image.manifest.v1+json"
    }

    init(store: BlobStore, port: Int = 5555) {
        self.store = store
        self.port = port
    }

    /// Accumulates in-progress chunked uploads keyed by upload UUID.
    private actor UploadBuffers {
        private var buffers: [String: Data] = [:]

        func start() -> String {
            let id = UUID().uuidString
            buffers[id] = Data()
            return id
        }

        func append(_ data: Data, to id: String) {
            buffers[id, default: Data()].append(data)
        }

        func take(_ id: String) -> Data? {
            buffers.removeValue(forKey: id)
        }
    }

    func run() async throws {
        let router = Router()
        router.addMiddleware {
            LogRequestsMiddleware(.info)
        }
        let store = self.store
        let uploads = self.uploads
        let maxBlobSize = Self.maxBlobSize
        let maxManifestSize = Self.maxManifestSize

        router.get("/v2") { _, _ in
            Response(status: .ok)
        }

        // Begin an upload session, or perform a monolithic push when `digest`
        // is supplied on the initial POST (the whole blob is the body).
        router.post("/v2/{repo}/blobs/uploads") { request, context -> Response in
            let repo = context.parameters.get("repo") ?? ""
            if let digest = request.uri.queryParameters["digest"].map(String.init) {
                let buffer = try await request.body.collect(upTo: maxBlobSize)
                do {
                    try store.writeBlob(Data(buffer.readableBytesView), expectedDigest: digest)
                } catch is BlobStore.BlobError {
                    return Response(status: .badRequest)
                }
                return Response(
                    status: .created,
                    headers: [.location: "/v2/\(repo)/blobs/\(digest)"]
                )
            }
            let id = await uploads.start()
            return Response(
                status: .accepted,
                headers: [
                    .location: "/v2/\(repo)/blobs/uploads/\(id)",
                    HTTPField.Name("Docker-Upload-UUID")!: id,
                ]
            )
        }

        // Chunk append (PATCH) — used by chunked pushers.
        router.patch("/v2/{repo}/blobs/uploads/{id}") { request, context -> Response in
            let repo = context.parameters.get("repo") ?? ""
            let id = context.parameters.get("id") ?? ""
            let buffer = try await request.body.collect(upTo: maxBlobSize)
            await uploads.append(Data(buffer.readableBytesView), to: id)
            return Response(
                status: .accepted,
                headers: [.location: "/v2/\(repo)/blobs/uploads/\(id)"]
            )
        }

        // Commit upload (PUT ...?digest=<d>), optionally with a final chunk body.
        router.put("/v2/{repo}/blobs/uploads/{id}") { request, context -> Response in
            let repo = context.parameters.get("repo") ?? ""
            let id = context.parameters.get("id") ?? ""
            let digest = request.uri.queryParameters["digest"].map(String.init) ?? ""
            var data = await uploads.take(id) ?? Data()
            let buffer = try await request.body.collect(upTo: maxBlobSize)
            data.append(Data(buffer.readableBytesView))
            do {
                try store.writeBlob(data, expectedDigest: digest)
            } catch is BlobStore.BlobError {
                return Response(status: .badRequest)
            }
            return Response(
                status: .created,
                headers: [.location: "/v2/\(repo)/blobs/\(digest)"]
            )
        }

        router.head("/v2/{repo}/blobs/{digest}") { _, context -> Response in
            let digest = context.parameters.get("digest") ?? ""
            guard BlobStore.isValidSHA256Digest(digest) else { return Response(status: .notFound) }
            guard store.hasBlob(digest: digest) else { return Response(status: .notFound) }
            return Response(
                status: .ok,
                headers: [
                    .contentType: "application/octet-stream",
                    Self.dockerContentDigest: digest,
                ]
            )
        }

        router.get("/v2/{repo}/blobs/{digest}") { _, context -> Response in
            let digest = context.parameters.get("digest") ?? ""
            guard BlobStore.isValidSHA256Digest(digest) else { return Response(status: .notFound) }
            guard let data = store.readBlob(digest: digest) else {
                return Response(status: .notFound)
            }
            return Response(
                status: .ok,
                headers: [
                    .contentType: "application/octet-stream",
                    Self.dockerContentDigest: digest,
                ],
                body: .init(byteBuffer: ByteBuffer(bytes: data))
            )
        }

        router.put("/v2/{repo}/manifests/{reference}") { request, context -> Response in
            let repo = context.parameters.get("repo") ?? ""
            let reference = context.parameters.get("reference") ?? ""
            guard BlobStore.isValidRepository(repo), BlobStore.isValidReference(reference) else {
                return Response(status: .badRequest)
            }
            let buffer = try await request.body.collect(upTo: maxManifestSize)
            try store.writeManifest(
                Data(buffer.readableBytesView),
                repository: repo,
                reference: reference
            )
            return Response(status: .created)
        }

        router.head("/v2/{repo}/manifests/{reference}") { _, context -> Response in
            let repo = context.parameters.get("repo") ?? ""
            let reference = context.parameters.get("reference") ?? ""
            guard BlobStore.isValidRepository(repo), BlobStore.isValidReference(reference) else {
                return Response(status: .notFound)
            }
            guard let url = store.manifestURL(repository: repo, reference: reference),
                let data = try? Data(contentsOf: url)
            else { return Response(status: .notFound) }
            return Response(
                status: .ok,
                headers: [
                    .contentType: Self.manifestMediaType(data),
                    Self.dockerContentDigest: Self.contentDigest(of: data),
                ]
            )
        }

        router.get("/v2/{repo}/manifests/{reference}") { _, context -> Response in
            let repo = context.parameters.get("repo") ?? ""
            let reference = context.parameters.get("reference") ?? ""
            guard BlobStore.isValidRepository(repo), BlobStore.isValidReference(reference) else {
                return Response(status: .notFound)
            }
            guard let url = store.manifestURL(repository: repo, reference: reference),
                let data = try? Data(contentsOf: url)
            else { return Response(status: .notFound) }
            // Content-Type must echo the stored manifest's mediaType; default to OCI.
            return Response(
                status: .ok,
                headers: [
                    .contentType: Self.manifestMediaType(data),
                    Self.dockerContentDigest: Self.contentDigest(of: data),
                ],
                body: .init(byteBuffer: ByteBuffer(bytes: data))
            )
        }

        let app = Application(
            router: router,
            configuration: .init(address: .hostname("127.0.0.1", port: port))
        )
        logger.info("Agent image registry listening", metadata: ["port": "\(port)"])
        try await app.runService()
    }
}
