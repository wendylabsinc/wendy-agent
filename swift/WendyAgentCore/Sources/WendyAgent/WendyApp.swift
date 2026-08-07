import Foundation

struct WendyApp: Codable {
    struct NativeMetadata: Codable, Equatable {
        var directory: String
        var binaryName: String
        var args: [String]
        var currentDirectory: String?
    }

    struct ContainerMetadata: Codable, Equatable {
        var imageName: String
        var appConfig: WendyAppConfig?
    }

    var info: WendyAppInfo
    var native: NativeMetadata?
    var container: ContainerMetadata?

    /// Persisted restart policy. Defaults to unless-stopped for `info.json`
    /// files written before this field existed.
    var restartPolicy: PersistedRestartPolicy
    /// Durable "the user asked this app to stop" flag. Set by the
    /// `StopContainer` RPC, cleared by `StartContainer`; agent-shutdown stops
    /// (`stopApp`/`stopAllApps`) must never set it, since a quit/update isn't
    /// user intent to keep the app stopped. Defaults to `false` for legacy
    /// `info.json` files.
    var stoppedByUser: Bool

    var process: Foundation.Process?
    var launchToken: UUID?

    /// Non-persisted supervisor bookkeeping (Task 2 consumes these); reset on
    /// every agent restart and on an explicit user start.
    var failureCount: Int = 0
    var lastRestart: Date? = nil
    var lastExitCode: Int32? = nil

    /// The pid `info.json` carried when this app was loaded, if any. `info.pid`
    /// is deliberately scrubbed on load (the agent has no handle on that
    /// process, and reporting it would attribute stats to a possibly-recycled
    /// pid), but reconcile still needs it to recognize a native app that
    /// survived a disorderly agent exit. Set only by `ContainerService.loadApps`
    /// and cleared once reconcile has considered it.
    var persistedPID: Int32? = nil

    enum CodingKeys: String, CodingKey {
        case info
        case native
        case container
        case restartPolicy
        case stoppedByUser
    }

    init(
        info: WendyAppInfo,
        native: NativeMetadata? = nil,
        container: ContainerMetadata? = nil,
        restartPolicy: PersistedRestartPolicy = .default,
        stoppedByUser: Bool = false,
        process: Foundation.Process? = nil,
        launchToken: UUID? = nil,
        failureCount: Int = 0,
        lastRestart: Date? = nil,
        lastExitCode: Int32? = nil,
        persistedPID: Int32? = nil
    ) {
        self.info = info
        self.native = native
        self.container = container
        self.restartPolicy = restartPolicy
        self.stoppedByUser = stoppedByUser
        self.process = process
        self.launchToken = launchToken
        self.failureCount = failureCount
        self.lastRestart = lastRestart
        self.lastExitCode = lastExitCode
        self.persistedPID = persistedPID
    }

    /// Custom decode so `restartPolicy`/`stoppedByUser` default sensibly when
    /// reading `info.json` files written before those keys existed, and so
    /// the non-persisted runtime fields always start fresh on load.
    /// `encode(to:)` is left to synthesis.
    init(from decoder: any Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        self.info = try values.decode(WendyAppInfo.self, forKey: .info)
        self.native = try values.decodeIfPresent(NativeMetadata.self, forKey: .native)
        self.container = try values.decodeIfPresent(ContainerMetadata.self, forKey: .container)
        self.restartPolicy =
            try values.decodeIfPresent(PersistedRestartPolicy.self, forKey: .restartPolicy)
            ?? .default
        self.stoppedByUser = try values.decodeIfPresent(Bool.self, forKey: .stoppedByUser) ?? false
        self.process = nil
        self.launchToken = nil
        self.failureCount = 0
        self.lastRestart = nil
        self.lastExitCode = nil
        self.persistedPID = nil
    }
}
