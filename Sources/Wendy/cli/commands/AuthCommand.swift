import ArgumentParser
import Foundation
import Noora

struct AuthCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "auth",
        abstract: "Managed authentication to cloud services",
        subcommands: [
            LoginCommand.self,
            LogoutCommand.self,
        ]
    )
}

struct LoginCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "login",
        abstract: "Log into cloud services"
    )
    
    @Option
    var cloud = "https://cloud.wendy.sh"
    
    func run() async throws {
        try await loginFlow(cloud: cloud) { token in
            #if canImport(Darwin)
            Task {
                try await Task.sleep(for: .seconds(1))
                Darwin.exit(0)
            }
            #endif
        }
    }
}

struct PairingCodeRule: ValidatableRule {
    var error: ValidatableError {
        "Code must be 6 alphanumeric characters"
    }
    
    func validate(input: String) -> Bool {
        input.count == 6 && input.allSatisfy(\.isASCII) && !input.contains(where: \.isWhitespace)
    }
}

struct LogoutCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "logout",
        abstract: "Log out of cloud services"
    )
    
    func run() async throws {
        var config = try getConfig()
        
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
