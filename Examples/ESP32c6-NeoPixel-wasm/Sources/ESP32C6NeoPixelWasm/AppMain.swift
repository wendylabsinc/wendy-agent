@_spi(ExperimentalCustomExecutors)
import WendyLite

// Use Wendy Lite's cooperative executor for responsive WASM host calls.
typealias DefaultExecutorFactory = WendyExecutorFactory

private let neoPixelPin: Int32 = 8
private let pixelCount: Int32 = 1
private let brightness: Int32 = 32
private let stepDelay: Duration = .milliseconds(750)

private func showColor(red: Int32, green: Int32, blue: Int32) {
    var pixel: Int32 = 0
    while pixel < pixelCount {
        NeoPixel.set(index: pixel, r: red, g: green, b: blue)
        pixel += 1
    }
}

@main
struct ESP32C6NeoPixelWasm: WendyLiteApp {
    let clock = WendyClock()
    var colorStep: UInt8 = 0
    var isReady = false

    mutating func setup() async {
        isReady = NeoPixel.initialize(
            pin: neoPixelPin,
            numLeds: pixelCount
        ) == 0
    }

    mutating func loop() async {
        if isReady {
            switch colorStep {
            case 0:
                showColor(red: brightness, green: 0, blue: 0)
            case 1:
                showColor(red: 0, green: brightness, blue: 0)
            case 2:
                showColor(red: 0, green: 0, blue: brightness)
            default:
                NeoPixel.clear()
            }

            colorStep = (colorStep + 1) % 4
        }

        try? await clock.sleep(for: stepDelay)
    }
}
