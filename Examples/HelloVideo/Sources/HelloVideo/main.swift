import Foundation
import Hummingbird
import LinuxVideo
import JPEG

// Create router and add routes
let router = Router()

struct ResponseBodyJPEGWriter: JPEG.Bytestream.Destination {
    let stream = AsyncStream<ByteBuffer>.makeStream()

    mutating func write(_ bytes: [UInt8]) -> Void? {
        stream.continuation.yield(ByteBuffer(bytes: bytes))
        return ()
    }
}

// Serve HTML page with video device information
router.get("/") { _, _ -> Response in
    var output = """
        <!DOCTYPE html>
        <html>
        <head>
            <title>Video Devices</title>
            <style>
                body { font-family: Arial, sans-serif; margin: 0; padding: 20px; }
                h1 { color: #333; }
                .device { margin-bottom: 20px; padding: 15px; border: 1px solid #ddd; border-radius: 5px; }
                .device h2 { margin-top: 0; }
                .property { margin: 5px 0; }
                .label { font-weight: bold; display: inline-block; width: 120px; }
            </style>
        </head>
        <body>
            <h1>Video Devices</h1>
        """

    do {
        let devices = try VideoDeviceManager.listDevices()

        if devices.isEmpty {
            output += "<p>No video devices found.</p>"
        } else {
            for (index, device) in devices.enumerated() {
                output += """
                    <div class="device">
                        <h2>Device \(index): \(device.path)</h2>
                        <div class="property"><span class="label">Name:</span> \(device.name)</div>
                        <div class="property"><span class="label">Driver:</span> \(device.driver)</div>
                        <div class="property"><span class="label">Bus Info:</span> \(device.busInfo)</div>
                        <div class="property"><span class="label">Capabilities:</span> \(String(format: "0x%08X", device.capabilities))</div>
                        <div class="property"><span class="label">Video Capture:</span> \(device.supportsCapture ? "Yes" : "No")</div>
                        <div class="property"><span class="label">Streaming:</span> \(device.supportsStreaming ? "Yes" : "No")</div>
                    </div>
                    """
            }
        }
    } catch {
        output += "<p>Error listing video devices: \(error)</p>"
    }

    output += """
            </body>
        </html>
        """

    return Response(status: .ok, headers: [
        .contentType: "text/html"
    ], body: ResponseBody(byteBuffer: ByteBuffer(string: output)))
}

// Add route to capture a frame
router.get("/capture") { req, context in
    let devices = try VideoDeviceManager.listDevices()
    guard let device = devices.first(where: { $0.supportsCapture }) else {
        throw VideoError.unsupportedOperation(message: "No video capture device found")
    }

    let width: UInt32 = req.uri.queryParameters["width"].flatMap {
        UInt32(String($0))
    } ?? 640
    let height: UInt32 = req.uri.queryParameters["height"].flatMap {
        UInt32(String($0))
    } ?? 480

    // Capture frame in RGB format
    var writer = ResponseBodyJPEGWriter()
    defer { writer.stream.continuation.finish() }
    let body = ResponseBody(asyncSequence: writer.stream.stream)
    try await device.captureRGBFrame(width: width, height: height, writer: &writer)

    // Set content type for raw RGB
    return Response(status: .ok, headers: [
        .contentType: "image/jpeg"
    ], body: body)
}

// List video devices in CLI as well
print("Scanning for video devices...")
VideoDeviceManager.printDeviceList()

// Create application using router
let app = Application(
    router: router,
    configuration: .init(address: .hostname("0.0.0.0", port: 8080))
)

// Run hummingbird application
try await app.runService()
