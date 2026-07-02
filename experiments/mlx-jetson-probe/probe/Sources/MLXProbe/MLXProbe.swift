import ArgumentParser
import Foundation
import MLX
import MLXLLM
import MLXLMCommon

/// Feasibility probe for MLX text generation on NVIDIA Jetson (CUDA).
///
/// Loads an MLX-format model from a local directory, generates from a
/// prompt, and prints tokens/second — comparable against the llama.cpp
/// numbers measured for HelloVLM (gemma-3-4b Q4: ~56 tok/s on AGX Thor).
@main
struct MLXProbe: AsyncParsableCommand {
    static let configuration = CommandConfiguration(commandName: "MLXProbe")

    @Option(name: .long, help: "Local path to an MLX model directory (safetensors + config).")
    var modelPath: String

    @Option(name: .long, help: "Prompt to generate from.")
    var prompt: String = "Explain in two sentences why unified memory matters for on-device AI."

    @Option(name: .long, help: "Maximum tokens to generate.")
    var maxTokens: Int = 200

    @Option(name: .long, help: "Device to run on: gpu or cpu.")
    var device: String = "gpu"

    mutating func run() async throws {
        switch device {
        case "gpu":
            Device.setDefault(device: .gpu)
        case "cpu":
            Device.setDefault(device: .cpu)
        default:
            throw ValidationError("--device must be gpu or cpu")
        }
        print("Device: \(device)")

        let modelDirectory = URL(fileURLWithPath: modelPath, isDirectory: true)
        print("Loading model: \(modelDirectory.path) …")
        let loadStartedAt = Date()
        let container = try await LLMModelFactory.shared.loadContainer(
            configuration: ModelConfiguration(directory: modelDirectory)
        ) { progress in
            let percent = Int(progress.fractionCompleted * 100)
            if percent % 25 == 0 {
                print("  Loading: \(percent)%")
            }
        }
        print(String(format: "Model loaded in %.1fs", Date().timeIntervalSince(loadStartedAt)))

        let userInput = UserInput(chat: [.user(prompt)])
        let lmInput = try await container.prepare(input: userInput)

        print("Prompt: \(prompt)")
        print("Generating …\n")
        let generateStartedAt = Date()
        var parameters = GenerateParameters()
        parameters.maxTokens = maxTokens

        let stream = try await container.generate(input: lmInput, parameters: parameters)
        for await generation in stream {
            switch generation {
            case .chunk(let text):
                print(text, terminator: "")
                fflush(stdout)
            case .info(let info):
                print("\n")
                print("=== RESULT ===")
                print(info.summary())
            default:
                break
            }
        }
        print(String(format: "total wall time: %.1fs", Date().timeIntervalSince(generateStartedAt)))
    }
}
