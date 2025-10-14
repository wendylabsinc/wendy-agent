import ArgumentParser
import Foundation
import Noora
import WendySDK
import Crypto
import WendyCloudGRPC
import X509
import SwiftASN1

struct AuthCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "auth",
        abstract: "Managed authentication to cloud services",
        subcommands: [
            LoginCommand.self,
            LogoutCommand.self,
            TestCommand.self,
        ]
    )
}

struct LoginCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "login",
        abstract: "Log into cloud services"
    )
    
    @Option
    var cloudDashboard = "https://cloud.wendy.sh"

    @Option
    var cloudGRPC = "https://cloud.wendy.sh"
    
    func run() async throws {
        try await loginFlow(
            cloudDashboard: cloudDashboard,
            cloudGRPC: cloudGRPC
        ) { token in
            Noora().success("Logged in")
            #if canImport(Darwin)
            Task {
                try await Task.sleep(for: .seconds(1))
                Darwin.exit(0)
            }
            #endif
        }
    }
}

struct TestCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "test",
        abstract: "Test the login flow"
    )
    
    func run() async throws {
        try await withCloudGRPCClient(title: "Test") { client in
            let certs = Wendycloud_V1_CertificateService.Client(wrapping: client.grpc)
            let response = try await certs.getCertificateMetadata(.init())
            print(response)
        }
    }
}

struct EnrollmentTokenRule: ValidatableRule {
    var error: ValidatableError {
        "Code must be 6 alphanumeric characters"
    }
    
    func validate(input: String) -> Bool {
        input.count >= 6 && !input.contains(where: \.isWhitespace)
    }
}

struct LogoutCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "logout",
        abstract: "Log out of cloud services"
    )
    
    func run() async throws {
        var config = try getConfig()

        if config.auth.isEmpty {
            Noora().error("No accounts found")
            return
        }
        
        let logout = Noora().singleChoicePrompt(
            title: "Logout",
            question: "Which account do you want to log out of?",
            options: config.auth
        )
        
        config.auth.removeAll { $0 == logout }
        
        let data = try JSONEncoder().encode(config)
        try data.write(to: configURL)
    }
}
