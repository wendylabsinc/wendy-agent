import Foundation
import GRPCCore
import Testing
import WendyAgentGRPC

@testable import WendyAgentCore

/// A fake backend that records calls and hands back a short-lived process.
actor FakeLinuxBackend: LinuxContainerBackend {
    private(set) var pulled: [String] = []
    private(set) var started: [String] = []
    private(set) var stopped: [String] = []
    private(set) var removed: [String] = []
    /// Retains the process handed to `ContainerService` so `stop`/`remove` can
    /// end it, mirroring a real backend where the attached process lives until
    /// the container is stopped.
    private var processes: [String: Foundation.Process] = [:]

    func pull(image: String) async throws { pulled.append(image) }

    func createAndStart(
        appName: String,
        imageName: String,
        appConfig: WendyAppConfig?,
        terminationHandler: (@Sendable (Foundation.Process) -> Void)?
    ) async throws -> (process: Foundation.Process, stdout: Pipe, stderr: Pipe) {
        started.append(appName)
        let p = Foundation.Process()
        // Long-lived: the app must be observably `.running` until the test stops
        // it. A short-lived process (e.g. `/bin/echo`) would exit immediately and
        // fire the termination handler, flipping the app back to `.stopped` before
        // the test could observe it running.
        p.executableURL = URL(fileURLWithPath: "/bin/sleep")
        p.arguments = ["30"]
        let out = Pipe()
        let err = Pipe()
        p.standardOutput = out
        p.standardError = err
        p.terminationHandler = terminationHandler
        try p.run()
        processes[appName] = p
        return (p, out, err)
    }

    func stop(appName: String) async throws {
        stopped.append(appName)
        // A real backend stops the container, which ends its attached process.
        processes[appName]?.terminate()
    }
    func remove(appName: String) async throws {
        removed.append(appName)
        processes[appName]?.terminate()
        processes[appName] = nil
    }
    func listContainers() async throws -> [LinuxContainerInfo] { [] }

    func pulledImages() -> [String] { pulled }
    func startedApps() -> [String] { started }
    func stoppedApps() -> [String] { stopped }
    func removedApps() -> [String] { removed }
}

@Suite struct ContainerServiceLinuxTests {
    @Test func createThenStartPullsAndRunsViaBackend() async throws {
        let backend = FakeLinuxBackend()
        let service = ContainerService(
            broadcaster: TelemetryBroadcaster(),
            executablePath: "/usr/bin/true",
            stateDirectory: FileManager.default.temporaryDirectory
                .appendingPathComponent("cs-\(UUID().uuidString)"),
            linuxBackend: backend
        )
        let config = WendyAppConfig(
            appId: "svc",
            platform: "linux/arm64",
            entitlements: nil,
            brewfile: nil
        )
        let configData = try JSONEncoder().encode(config)

        // createContainer registers a .container app (no throw).
        var createReq = Wendy_Agent_Services_V1_CreateContainerRequest()
        createReq.appName = "svc"
        createReq.imageName = "localhost:5555/svc:latest"
        createReq.appConfig = configData
        _ = try await service.createContainer(
            request: ServerRequest(metadata: [:], message: createReq),
            context: makeServerContext(method: "CreateContainer")
        )
        let infos = await service.currentAppInfosForTesting()
        #expect(infos.contains { $0.id == "svc" && $0.kind == .container })

        // startContainer pulls the image, then creates+starts via the backend.
        // The streaming producer blocks until the container's process exits, so
        // drive it on a background task: it emits `.started` while the app runs
        // and returns once stopContainer ends the process.
        var startReq = Wendy_Agent_Services_V1_StartContainerRequest()
        startReq.appName = "svc"
        let response = try await service.startContainer(
            request: ServerRequest(metadata: [:], message: startReq),
            context: makeServerContext(method: "StartContainer")
        )

        let contents = try response.accepted.get()
        let writer = CollectingWriter<Wendy_Agent_Services_V1_RunContainerLayersResponse>()
        let produce = contents.producer
        let producer = Task {
            try await produce(RPCWriter(wrapping: writer))
        }

        // markAppRunning completes inside startContainer, so the app is running
        // by the time the call returns and stays so until it is stopped.
        let runningInfo = try #require(await service.appInfo(forAppID: "svc"))
        #expect(runningInfo.status == .running)

        #expect(await backend.pulledImages() == ["localhost:5555/svc:latest"])
        #expect(await backend.startedApps() == ["svc"])

        // stopContainer routes to the backend's stop(appName:), which ends the
        // container's process and lets the streaming producer return.
        var stopReq = Wendy_Agent_Services_V1_StopContainerRequest()
        stopReq.appName = "svc"
        _ = try await service.stopContainer(
            request: ServerRequest(metadata: [:], message: stopReq),
            context: makeServerContext(method: "StopContainer")
        )
        #expect(await backend.stoppedApps() == ["svc"])

        // The producer has now returned; it streamed a `.started` message.
        _ = try await producer.value
        let messages = writer.snapshot()
        #expect(
            messages.contains { message in
                if case .started = message.responseType { return true }
                return false
            }
        )

        // deleteContainer routes to the backend's remove(appName:).
        var deleteReq = Wendy_Agent_Services_V1_DeleteContainerRequest()
        deleteReq.appName = "svc"
        _ = try await service.deleteContainer(
            request: ServerRequest(metadata: [:], message: deleteReq),
            context: makeServerContext(method: "DeleteContainer")
        )
        #expect(await backend.removedApps() == ["svc"])
        #expect(await service.appInfo(forAppID: "svc") == nil)
    }

    @Test func createContainerFailsPreconditionWithoutABackend() async throws {
        let service = ContainerService(
            broadcaster: TelemetryBroadcaster(),
            executablePath: "/usr/bin/true",
            stateDirectory: FileManager.default.temporaryDirectory
                .appendingPathComponent("cs-\(UUID().uuidString)")
        )
        let config = WendyAppConfig(
            appId: "svc",
            platform: "linux/arm64",
            entitlements: nil,
            brewfile: nil
        )
        var createReq = Wendy_Agent_Services_V1_CreateContainerRequest()
        createReq.appName = "svc"
        createReq.imageName = "localhost:5555/svc:latest"
        createReq.appConfig = try JSONEncoder().encode(config)

        do {
            _ = try await service.createContainer(
                request: ServerRequest(metadata: [:], message: createReq),
                context: makeServerContext(method: "CreateContainer")
            )
            Issue.record("Expected createContainer to fail precondition without a Linux backend")
        } catch let error as RPCError {
            #expect(error.code == .failedPrecondition)
            #expect("\(error)".contains("No Linux container runtime found"))
        }
    }
}

// MARK: - Helpers

private func makeServerContext(method: String) -> ServerContext {
    ServerContext(
        descriptor: MethodDescriptor(
            fullyQualifiedService: "wendy.agent.services.v1.WendyContainerService",
            method: method
        ),
        remotePeer: "in-process:test",
        localPeer: "in-process:test",
        cancellation: .init()
    )
}

private final class CollectingWriter<Element: Sendable>: RPCWriterProtocol, @unchecked Sendable {
    private let queue = DispatchQueue(label: "wendy.tests.linux-collecting-writer")
    private var elements: [Element] = []

    func write(_ element: Element) async throws {
        queue.sync {
            elements.append(element)
        }
    }

    func write(contentsOf elements: some Sequence<Element>) async throws {
        queue.sync {
            self.elements.append(contentsOf: elements)
        }
    }

    func snapshot() -> [Element] {
        queue.sync {
            elements
        }
    }
}
