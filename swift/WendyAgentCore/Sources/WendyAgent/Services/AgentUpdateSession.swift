import CryptoKit
import Foundation
import GRPCCore
import Logging
import WendyAgentGRPC

/// Seam hiding the `@available(macOS 26)` ML-DSA verifier so the update
/// session stays testable (and buildable/runnable below macOS 26).
protocol UpdateSignatureChecking: Sendable {
    /// Verifies the detached signature over the 32-byte SHA-256 digest of the
    /// update payload. Throws `SignatureVerifierError` when the payload is
    /// rejected; returns normally when it is accepted (or when verification is
    /// disabled).
    func verify(digest: Data, signature: Data) throws
}

/// Production checker: defers to the build-embedded `SignatureVerifier`.
///
/// ML-DSA65 requires macOS 26, so below that there is nothing to verify with
/// and the check is skipped — the same fail-safe skip the Go agent performs
/// while no pinned key is embedded (`SignatureVerifier.default.isEnabled` is
/// `false` today, so even on macOS 26 this is currently a no-op).
struct DefaultUpdateSignatureChecker: UpdateSignatureChecking {
    func verify(digest: Data, signature: Data) throws {
        if #available(macOS 26.0, *) {
            try SignatureVerifier.default.verify(message: digest, signature: signature)
        }
    }
}

/// Drives one `UpdateAgent` bidi stream: receives the zipped `.app` payload,
/// verifies its digest and signature, extracts and code-signature-gates the
/// incoming bundle, swaps it into place, and schedules the relaunch.
enum AgentUpdateSession {
    /// Parity with the Go agent's `maxAgentBinarySize`.
    static let maxPayloadBytes: Int64 = 256 * 1024 * 1024

    struct Dependencies: Sendable {
        var stagingRoot: URL = WendyAgentPaths.agentUpdateStagingDirectory
        var maxPayloadBytes: Int64 = AgentUpdateSession.maxPayloadBytes
        var currentBundleURL: URL = Bundle.main.bundleURL
        var codesign: any CodesignVerifying = SecStaticCodeVerifier()
        var signature: any UpdateSignatureChecking = DefaultUpdateSignatureChecker()
        var installer: any AgentBundleInstalling = DittoAgentBundleInstaller()
        var relauncher: any AgentRelaunchScheduling
        /// Invoked at the point of no return — the new bundle is installed and
        /// the relaunch/termination is about to be scheduled. `AgentService`
        /// wires this to `AgentUpdateLock.markCommitted()` so the update lock
        /// can no longer be stolen while the process shuts down.
        var onCommitted: (@Sendable () async -> Void)?
    }

    /// Runs the session to completion. Returns normally only after a committed
    /// install (the process is expected to exit shortly afterwards); every
    /// other outcome throws an `RPCError`.
    ///
    /// The staging directory for this session is always removed before
    /// returning or rethrowing, so a failed update never leaks a
    /// several-hundred-MiB payload into Application Support.
    static func run<Messages: AsyncSequence & Sendable>(
        messages: Messages,
        writeResponse: (Wendy_Agent_Services_V1_UpdateAgentResponse) async throws -> Void,
        deps: Dependencies,
        logger: Logger
    ) async throws where Messages.Element == Wendy_Agent_Services_V1_UpdateAgentRequest {
        Self.removeStaleSessions(in: deps.stagingRoot, logger: logger)

        let sessionDir = deps.stagingRoot.appendingPathComponent(
            UUID().uuidString,
            isDirectory: true
        )
        do {
            try FileManager.default.createDirectory(
                at: sessionDir,
                withIntermediateDirectories: true
            )
        } catch {
            throw RPCError(
                code: .internalError,
                message: "failed to create update staging directory: \(error)"
            )
        }

        do {
            try await Self.runStaged(
                messages: messages,
                writeResponse: writeResponse,
                sessionDir: sessionDir,
                deps: deps,
                logger: logger
            )
        } catch {
            try? FileManager.default.removeItem(at: sessionDir)
            throw error
        }
        try? FileManager.default.removeItem(at: sessionDir)
    }

    // MARK: - Session body

    private static func runStaged<Messages: AsyncSequence & Sendable>(
        messages: Messages,
        writeResponse: (Wendy_Agent_Services_V1_UpdateAgentResponse) async throws -> Void,
        sessionDir: URL,
        deps: Dependencies,
        logger: Logger
    ) async throws where Messages.Element == Wendy_Agent_Services_V1_UpdateAgentRequest {
        let payloadURL = sessionDir.appendingPathComponent("payload.zip")
        guard FileManager.default.createFile(atPath: payloadURL.path, contents: nil),
            let payloadHandle = FileHandle(forWritingAtPath: payloadURL.path)
        else {
            throw RPCError(
                code: .internalError,
                message: "failed to open update staging file at \(payloadURL.path)"
            )
        }
        // Cleared once the handle is closed explicitly, so the defer does not
        // close an already-closed handle.
        var openHandle: FileHandle? = payloadHandle
        defer { try? openHandle?.close() }

        var hasher = SHA256()
        var received: Int64 = 0
        var messageIterator = messages.makeAsyncIterator()

        while let message = try await messageIterator.next() {
            switch message.requestType {
            case .chunk(let chunk):
                let data = chunk.data
                guard received + Int64(data.count) <= deps.maxPayloadBytes else {
                    throw RPCError(
                        code: .resourceExhausted,
                        message:
                            "update stream exceeds maximum agent bundle size (\(deps.maxPayloadBytes >> 20) MiB)"
                    )
                }
                do {
                    try payloadHandle.write(contentsOf: data)
                } catch {
                    throw RPCError(
                        code: .internalError,
                        message: "failed to write update chunk: \(error)"
                    )
                }
                hasher.update(data: data)
                received += Int64(data.count)

            case .control(let control):
                guard case .update(let update) = control.command else {
                    throw RPCError(
                        code: .invalidArgument,
                        message: "unsupported update control command"
                    )
                }

                let digest = Data(hasher.finalize())
                try Self.verifyDigest(digest, against: update.sha256)
                try Self.verifySignature(
                    digest: digest,
                    signature: update.signature,
                    checker: deps.signature
                )

                // The payload must be fully flushed before `ditto` reads it.
                do {
                    try payloadHandle.close()
                    openHandle = nil
                } catch {
                    throw RPCError(
                        code: .internalError,
                        message: "failed to finalize update payload: \(error)"
                    )
                }

                logger.info(
                    "Installing agent update",
                    metadata: ["bytes": "\(received)", "sha256": "\(Self.hexString(digest))"]
                )
                try await Self.install(payloadURL: payloadURL, sessionDir: sessionDir, deps: deps)
                logger.info(
                    "Agent bundle replaced",
                    metadata: [
                        "bundle": "\(deps.currentBundleURL.path)",
                        "version": "\(Self.bundleVersion(at: deps.currentBundleURL) ?? "unknown")",
                    ]
                )

                await Self.commitTail(deps: deps, logger: logger)
                // Parity with the Go agent's `finishCommittedUpdate`: the
                // restart is already scheduled, so a client that hangs up
                // before the ack lands must not turn a committed install into
                // a failed RPC.
                do {
                    var response = Wendy_Agent_Services_V1_UpdateAgentResponse()
                    response.responseType = .updated(
                        Wendy_Agent_Services_V1_UpdateAgentResponse.Updated()
                    )
                    try await writeResponse(response)
                } catch {
                    logger.warning(
                        "agent update committed but ack not delivered; restarting anyway",
                        metadata: ["error": "\(error)"]
                    )
                }
                return

            case nil:
                throw RPCError(
                    code: .invalidArgument,
                    message: "unexpected empty message in update stream"
                )
            }
        }

        throw RPCError(
            code: .invalidArgument,
            message: "update stream ended without update control command"
        )
    }

    // MARK: - Steps

    private static func verifyDigest(_ digest: Data, against expected: String) throws {
        guard expected.count == 64, expected.allSatisfy({ $0.isASCII && $0.isHexDigit }) else {
            throw RPCError(
                code: .invalidArgument,
                message: "update control command is missing a valid SHA256"
            )
        }
        let computed = Self.hexString(digest)
        guard expected.caseInsensitiveCompare(computed) == .orderedSame else {
            throw RPCError(
                code: .dataLoss,
                message: "SHA256 mismatch: expected \(expected), got \(computed)"
            )
        }
    }

    private static func verifySignature(
        digest: Data,
        signature: Data,
        checker: any UpdateSignatureChecking
    ) throws {
        do {
            try checker.verify(digest: digest, signature: signature)
        } catch SignatureVerifierError.unsigned {
            throw RPCError(
                code: .failedPrecondition,
                message: "agent update is unsigned; refusing install"
            )
        } catch SignatureVerifierError.badSignature {
            throw RPCError(
                code: .dataLoss,
                message: "agent update signature verification failed; refusing install"
            )
        } catch {
            throw RPCError(
                code: .internalError,
                message: "agent update signature verification error: \(error)"
            )
        }
    }

    /// Extracts the payload, applies the code-signature policy, and swaps the
    /// incoming bundle into place.
    private static func install(
        payloadURL: URL,
        sessionDir: URL,
        deps: Dependencies
    ) async throws {
        // Extract into a dedicated subdirectory instead of `sessionDir`: the
        // payload sits at `sessionDir/payload.zip`, and an archive entry with
        // that same name would otherwise overwrite the source mid-extract.
        // `extractBundle` creates the directory itself.
        let extractDir = sessionDir.appendingPathComponent("extract", isDirectory: true)

        let stagedApp: URL
        do {
            stagedApp = try await deps.installer.extractBundle(zipAt: payloadURL, into: extractDir)
        } catch let error as AgentBundleInstallerError {
            throw Self.rpcError(for: error)
        } catch {
            // e.g. the staging directory or `ditto` itself could not be
            // spawned — an agent-side failure, not a bad payload.
            throw RPCError(
                code: .internalError,
                message: "failed to extract the update payload: \(error)"
            )
        }

        let incoming: CodesignInfo
        do {
            incoming = try await deps.codesign.inspect(bundleAt: stagedApp)
        } catch CodesignVerificationError.invalidSignature(let detail) {
            throw RPCError(
                code: .failedPrecondition,
                message: "incoming agent bundle failed code signature verification: \(detail)"
            )
        } catch {
            throw RPCError(
                code: .internalError,
                message: "could not inspect the incoming agent bundle: \(Self.detail(of: error))"
            )
        }

        let running: CodesignInfo
        do {
            running = try await deps.codesign.inspect(bundleAt: deps.currentBundleURL)
        } catch {
            // A running bundle that cannot be inspected is an agent-side
            // problem — never report it as if the client sent something bad.
            throw RPCError(
                code: .internalError,
                message: "could not inspect the running agent bundle: \(Self.detail(of: error))"
            )
        }

        if let policyError = AgentUpdateCodesignPolicy.check(incoming: incoming, running: running) {
            throw policyError
        }

        do {
            try await deps.installer.replaceBundle(at: deps.currentBundleURL, with: stagedApp)
        } catch let error as AgentBundleInstallerError {
            throw Self.rpcError(for: error)
        } catch {
            throw RPCError(
                code: .internalError,
                message: "failed to install updated app bundle: \(error)"
            )
        }
    }

    /// Schedules the relaunch and this process's termination. Runs *before*
    /// the ack is written so a failed ack cannot strand the agent in the
    /// "installed but never restarted" state.
    private static func commitTail(deps: Dependencies, logger: Logger) async {
        // Announce the commit before anything is scheduled: from here on the
        // update lock must not be stealable, because the process is on its way
        // out and a second update starting now could install over the bundle
        // that was just swapped in.
        await deps.onCommitted?()
        do {
            try deps.relauncher.scheduleRelaunch(of: deps.currentBundleURL)
        } catch {
            logger.error(
                "Failed to schedule agent relaunch; the agent will exit without reopening",
                metadata: ["error": "\(error)"]
            )
        }
        deps.relauncher.scheduleTermination()
    }

    // MARK: - Helpers

    private static func rpcError(for error: AgentBundleInstallerError) -> RPCError {
        switch error {
        case .notAnAppArchive(let detail):
            RPCError(
                code: .invalidArgument,
                message: "update payload is not a valid app archive: \(detail)"
            )
        case .invalidBundle(let detail):
            RPCError(
                code: .invalidArgument,
                message: "update payload is not a valid agent app bundle: \(detail)"
            )
        case .parentNotWritable(let detail):
            RPCError(code: .permissionDenied, message: detail)
        case .swapFailed(let detail):
            // `.swapFailed` details already state whether the destination was
            // touched or rolled back — pass them through verbatim.
            RPCError(
                code: .internalError,
                message: "failed to install updated app bundle: \(detail)"
            )
        }
    }

    private static func detail(of error: any Error) -> String {
        guard let codesignError = error as? CodesignVerificationError else { return "\(error)" }
        switch codesignError {
        case .invalidSignature(let detail), .inspectionFailed(let detail):
            return detail
        }
    }

    /// Best-effort removal of staging directories left behind by earlier
    /// interrupted attempts (e.g. a crash mid-upload).
    private static func removeStaleSessions(in stagingRoot: URL, logger: Logger) {
        guard
            let entries = try? FileManager.default.contentsOfDirectory(
                at: stagingRoot,
                includingPropertiesForKeys: nil
            )
        else {
            return
        }
        for entry in entries {
            do {
                try FileManager.default.removeItem(at: entry)
            } catch {
                logger.debug(
                    "Could not remove stale agent update staging entry",
                    metadata: ["path": "\(entry.path)", "error": "\(error)"]
                )
            }
        }
    }

    private static func bundleVersion(at bundleURL: URL) -> String? {
        let infoPlistURL = bundleURL.appendingPathComponent("Contents/Info.plist")
        guard let data = FileManager.default.contents(atPath: infoPlistURL.path),
            let parsed = try? PropertyListSerialization.propertyList(
                from: data,
                options: [],
                format: nil
            ),
            let plist = parsed as? [String: Any]
        else {
            return nil
        }
        return plist["WLWendyAgentVersion"] as? String
    }

    private static func hexString(_ data: Data) -> String {
        data.map { String(format: "%02x", $0) }.joined()
    }
}
