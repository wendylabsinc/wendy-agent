//
//  DockerCLI.swift
//  edge-agent
//
//  Created by Joannis Orlandos on 16/09/2025.
//

import Foundation
import Subprocess

/// Represents the Docker CLI interface for managing container images and running containers.
public struct DockerCLI: Sendable {
    public let command: String

    public init(command: String = "docker") {
        self.command = command
    }

    /// Build a Docker container.
    public func build(
        name: String,
        directory: String = "."
    ) async throws {
        _ = try await Subprocess.run(
            Subprocess.Executable.name(self.command),
            arguments: Subprocess.Arguments(["build", "-t", name, directory]),
            output: .discarded
        )
    }

    /// Export a Docker container.
    public func save(
        name: String,
        output: String
    ) async throws {
        _ = try await Subprocess.run(
            Subprocess.Executable.name(self.command),
            arguments: Subprocess.Arguments(["save", name, "-o", output]),
            output: .discarded
        )
    }
}
