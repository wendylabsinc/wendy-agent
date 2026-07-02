import ArgumentParser
import Foundation
import GStreamer
import MLX
import MLXLMCommon
import MLXVLM

/// Vision feasibility probe for MLX VLMs on NVIDIA Jetson: captures one
/// camera frame via GStreamer, feeds it to a gemma-3 VLM as a raw RGB
/// MLXArray (the Linux image path from mlx-swift-lm kb/linux-image-path),
/// and prints the description plus tokens/second.
@main
struct MLXVisionProbe: AsyncParsableCommand {
    static let configuration = CommandConfiguration(commandName: "MLXVisionProbe")

    @Option(name: .long, help: "Local path to an MLX VLM model directory.")
    var modelPath: String

    @Option(name: .long, help: "Prompt to run against the camera frame.")
    var prompt: String = "Describe what you see in this webcam frame in two sentences."

    @Option(name: .long, help: "V4L2 camera device.")
    var cameraDevice: String = "/dev/video0"

    @Option(name: .long, help: "Edge length frames are scaled to (the model's native size).")
    var imageSize: Int = 896

    @Option(name: .long, help: "Frames to discard while camera exposure settles.")
    var warmupFrames: Int = 10

    @Option(name: .long, help: "Maximum tokens to generate.")
    var maxTokens: Int = 200

    mutating func run() async throws {
        // GStreamer scales/converts to the model's native input so the MLX
        // side needs no resize: RGB, tightly packed (videoconvert), square.
        let pipeline = try Pipeline(
            """
            v4l2src device=\(cameraDevice) ! \
            video/x-raw,width=640,height=480 ! \
            videoconvert ! videoscale ! \
            video/x-raw,format=RGB,width=\(imageSize),height=\(imageSize) ! \
            appsink name=sink
            """)
        let sink = try AppSink(pipeline: pipeline, name: "sink")
        try pipeline.play()

        print("Capturing frame from \(cameraDevice) …")
        var captured: MLXArray?
        var seen = 0
        for try await frame in sink.frames() {
            seen += 1
            if seen <= warmupFrames { continue }

            let expected = frame.width * frame.height * 3
            captured = try frame.withUnsafeBytes { buffer -> MLXArray in
                guard buffer.count == expected else {
                    throw ValidationError(
                        "unexpected buffer size \(buffer.count), expected \(expected) — pixel format/stride mismatch")
                }
                return MLXArray([UInt8](buffer), [frame.height, frame.width, 3])
            }
            print("Captured \(frame.width)x\(frame.height) \(frame.format) after \(seen) frames")
            break
        }
        pipeline.stop()

        guard let image = captured else {
            throw ValidationError("no frame captured")
        }

        let modelDirectory = URL(fileURLWithPath: modelPath, isDirectory: true)
        print("Loading model: \(modelDirectory.path) …")
        let loadStartedAt = Date()
        let container = try await VLMModelFactory.shared.loadContainer(
            configuration: ModelConfiguration(directory: modelDirectory)
        ) { _ in }
        print(String(format: "Model loaded in %.1fs", Date().timeIntervalSince(loadStartedAt)))

        let userInput = UserInput(chat: [.user(prompt, images: [.array(image)])])
        let lmInput = try await container.prepare(input: userInput)

        print("Prompt: \(prompt)")
        print("Generating …\n")
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
    }
}
