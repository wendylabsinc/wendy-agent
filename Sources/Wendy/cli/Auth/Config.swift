import Foundation
import Hummingbird
import Noora
import JWTKit
#if os(macOS)
import AppKit
import Darwin
#endif

struct TokenWithSubject: JWTPayload {
    let sub: String
    
    func verify(using algorithm: some JWTKit.JWTAlgorithm) async throws {}
}

public struct Config: Sendable, Codable {
    public struct Auth: Sendable, Codable, Hashable, CustomStringConvertible {
        public var token: String
        public var expires: Date
        public let cloudDashboard: String
        public let cloudGRPC: String
        public let user: String
        
        public var description: String {
            user
        }
    }
    
    public var auth: [Auth]
    
    public init() {
        self.auth = []
    }
}

var configURL: URL {
    get throws {
        let wendyURL = FileManager.default
            .homeDirectoryForCurrentUser
            .appendingPathComponent(".wendy")
        
        try FileManager.default.createDirectory(at: wendyURL, withIntermediateDirectories: true)
        
        return wendyURL.appendingPathComponent("config.json")
    }
}

func getConfig() throws -> Config {
    do {
        let data = try Data(contentsOf: configURL)
        return try JSONDecoder().decode(Config.self, from: data)
    } catch {
        return Config()
    }
}

func withAuth(
    title: TerminalText,
    perform: @Sendable @escaping (Config.Auth) async throws -> Void
) async throws {
    let config = try getConfig()
    
    if config.auth.isEmpty {
        var cloudDashboard = Noora().textPrompt(
            title: title,
            prompt: "Enter the cloud dashboard URL",
            collapseOnAnswer: false
        )

        var cloudGRPC = Noora().textPrompt(
            title: title,
            prompt: "Enter the cloud gRPC URL",
            collapseOnAnswer: false
        )

        if cloudDashboard.isEmpty {
            cloudDashboard = "https://cloud.wendy.sh"
        } else if !cloudDashboard.contains("://") {
            cloudDashboard = "https://" + cloudDashboard
        }

        if cloudGRPC.isEmpty {
            cloudGRPC = "cloud.wendy.sh"
        }

        return try await loginFlow(
            cloudDashboard: cloudDashboard,
            cloudGRPC: cloudGRPC,
            withAuth: perform
        )
    } else if config.auth.count == 1 {
        return try await perform(config.auth[0])
    } else {
        let account = Noora().singleChoicePrompt(
            title: title,
            question: "Which account do you want to use?",
            options: config.auth
        )
        return try await perform(account)
    }
}

func loginFlow(
    cloudDashboard: String,
    cloudGRPC: String,
    withAuth: @Sendable @escaping (Config.Auth) async throws -> Void
) async throws {
    let router = Router().get("cli-callback") { req, context in
        do {
            let token = try req.uri.queryParameters.require("token")
            let expiresIn = try req.uri.queryParameters.require("expires_in", as: Int.self)
            
            var config = try getConfig()
            
            // TODO: Check if subject already exists
            
            let jwt: TokenWithSubject = try await JWTKeyCollection().unverified(token)
            let subject = jwt.sub
            
            config.auth.removeAll {
                $0.user == subject &&
                $0.cloudDashboard == cloudDashboard &&
                $0.cloudGRPC == cloudGRPC
            }

            let auth = Config.Auth(
                token: token,
                expires: Date().addingTimeInterval(TimeInterval(expiresIn)),
                cloudDashboard: cloudDashboard,
                cloudGRPC: cloudGRPC,
                user: subject
            )
            
            config.auth.append(auth)
            
            let data = try JSONEncoder().encode(config)
            try data.write(to: configURL)
            
            try await withAuth(auth)
            
            return Response(
                status: .ok,
                body: ResponseBody(byteBuffer: ByteBuffer(string: "Enrolled!"))
            )
        } catch {
            return Response(
                status: .badRequest,
                body: ResponseBody(byteBuffer: ByteBuffer(string: "Provisioning failed: \(error)"))
            )
        }
    }
    
    let server = Application(
        router: router,
        configuration: .init(
            address: .hostname("127.0.0.1", port: 0)
        ),
        onServerRunning: { channel in
            let port = channel.localAddress!.port!
            let url = "\(cloudDashboard)/cli-auth?redirect_uri=http://localhost:\(port)/cli-callback"
            #if os(macOS)
            if NSWorkspace.shared.open(URL(string: url)!) {
                Noora().info("""
                Open the following link in your browser:
                > \(cloudDashboard)/cli-auth?redirect_uri=http://localhost:\(port)/cli-callback
                """)
                return
            }
            #endif
            
            let code = Noora().textPrompt(
                title: """
                Open the following link in your browser:
                > \(cloudDashboard)/cli-auth
                """,
                prompt: "Provide the pairing code",
                collapseOnAnswer: false,
                validationRules: [
                    PairingCodeRule()
                ]
            )
        }
    )
    
    try await server.runService()
}
