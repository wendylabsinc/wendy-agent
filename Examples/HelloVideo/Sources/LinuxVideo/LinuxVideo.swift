import CLinuxVideo
import Foundation

// V4L2 capability constants
// These must match the values from linux/videodev2.h
public let V4L2_CAP_VIDEO_CAPTURE: UInt32 = 0x0000_0001
public let V4L2_CAP_STREAMING: UInt32 = 0x0400_0000

// V4L2 buffer types
public let V4L2_BUF_TYPE_VIDEO_CAPTURE: UInt32 = 1

// V4L2 memory types
public let V4L2_MEMORY_MMAP: UInt32 = 1

// V4L2 pixel formats
public let V4L2_PIX_FMT_YUYV: UInt32 = 0x5659_5559  // 'YUYV'

// V4L2 ioctl command values
// These are the evaluated values of the macros in videodev2.h
public let VIDIOC_QUERYCAP: UInt = 0x8068_5600  // _IOR('V', 0, struct v4l2_capability)
public let VIDIOC_S_FMT: UInt = 0xC0D0_5605  // _IOWR('V', 5, struct v4l2_format)
public let VIDIOC_REQBUFS: UInt = 0xC014_5608  // _IOWR('V', 8, struct v4l2_requestbuffers)
public let VIDIOC_QBUF: UInt = 0xC014_5609  // _IOWR('V', 9, struct v4l2_buffer)
public let VIDIOC_DQBUF: UInt = 0xC014_560A  // _IOWR('V', 10, struct v4l2_buffer)
public let VIDIOC_STREAMON: UInt = 0x4004_5614  // _IOW('V', 20, int)
public let VIDIOC_STREAMOFF: UInt = 0x4004_5615  // _IOW('V', 21, int)

/// A structure representing a V4L2 video device
public struct VideoDevice {
    /// The device path (e.g., "/dev/video0")
    public let path: String

    /// The device name
    public let name: String

    /// The device driver name
    public let driver: String

    /// The device bus information
    public let busInfo: String

    /// The device capabilities
    public let capabilities: UInt32

    /// Whether the device supports video capture
    public var supportsCapture: Bool {
        return (capabilities & V4L2_CAP_VIDEO_CAPTURE) != 0
    }

    /// Whether the device supports streaming I/O
    public var supportsStreaming: Bool {
        return (capabilities & V4L2_CAP_STREAMING) != 0
    }

    /// Capture a single frame from the video device
    /// - Parameters:
    ///   - width: The desired frame width
    ///   - height: The desired frame height
    /// - Returns: The captured frame data
    public func captureFrame(width: UInt32 = 640, height: UInt32 = 480) throws -> Data {
        // Open the device
        let fd = open(path, O_RDWR)
        if fd < 0 {
            throw VideoError.deviceOpenFailed(path: path, errno: errno)
        }
        defer { close(fd) }

        // Set up format
        var format = [UInt8](repeating: 0, count: 200)  // Large enough for v4l2_format
        // Set type field (offset 0)
        format.withUnsafeMutableBytes { ptr in
            ptr.storeBytes(of: V4L2_BUF_TYPE_VIDEO_CAPTURE, as: UInt32.self)
        }
        // Set width (offset 4 + 4)
        format.withUnsafeMutableBytes { ptr in
            ptr.storeBytes(of: width, toByteOffset: 8, as: UInt32.self)
        }
        // Set height (offset 4 + 8)
        format.withUnsafeMutableBytes { ptr in
            ptr.storeBytes(of: height, toByteOffset: 12, as: UInt32.self)
        }
        // Set pixel format (offset 4 + 12)
        format.withUnsafeMutableBytes { ptr in
            ptr.storeBytes(of: V4L2_PIX_FMT_YUYV, toByteOffset: 16, as: UInt32.self)
        }

        // Set format
        var result = ioctl(fd, UInt(VIDIOC_S_FMT), &format)
        if result < 0 {
            throw VideoError.ioctlFailed(command: "VIDIOC_S_FMT", errno: errno)
        }

        // Request buffers
        var reqbuf = [UInt8](repeating: 0, count: 16)  // v4l2_requestbuffers
        reqbuf.withUnsafeMutableBytes { ptr in
            ptr.storeBytes(of: UInt32(1), as: UInt32.self)  // count
            ptr.storeBytes(of: V4L2_MEMORY_MMAP, toByteOffset: 4, as: UInt32.self)  // memory
            ptr.storeBytes(of: V4L2_BUF_TYPE_VIDEO_CAPTURE, toByteOffset: 8, as: UInt32.self)  // type
        }

        result = ioctl(fd, UInt(VIDIOC_REQBUFS), &reqbuf)
        if result < 0 {
            throw VideoError.ioctlFailed(command: "VIDIOC_REQBUFS", errno: errno)
        }

        // Query buffer
        var buffer = [UInt8](repeating: 0, count: 32)  // v4l2_buffer
        buffer.withUnsafeMutableBytes { ptr in
            ptr.storeBytes(of: V4L2_BUF_TYPE_VIDEO_CAPTURE, as: UInt32.self)  // type
            ptr.storeBytes(of: V4L2_MEMORY_MMAP, toByteOffset: 8, as: UInt32.self)  // memory
            ptr.storeBytes(of: UInt32(0), toByteOffset: 4, as: UInt32.self)  // index
        }

        result = ioctl(fd, UInt(VIDIOC_QBUF), &buffer)
        if result < 0 {
            throw VideoError.ioctlFailed(command: "VIDIOC_QBUF", errno: errno)
        }

        // Start streaming
        var type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        result = ioctl(fd, UInt(VIDIOC_STREAMON), &type)
        if result < 0 {
            throw VideoError.ioctlFailed(command: "VIDIOC_STREAMON", errno: errno)
        }

        // Dequeue buffer
        result = ioctl(fd, UInt(VIDIOC_DQBUF), &buffer)
        if result < 0 {
            throw VideoError.ioctlFailed(command: "VIDIOC_DQBUF", errno: errno)
        }

        // Get buffer length and offset from the buffer structure
        let length = buffer.withUnsafeBytes { ptr in
            ptr.load(fromByteOffset: 12, as: UInt32.self)  // length field
        }
        let offset = buffer.withUnsafeBytes { ptr in
            ptr.load(fromByteOffset: 16, as: UInt32.self)  // m.offset field
        }

        // Map the buffer
        guard
            let data = mmap(nil, Int(length), PROT_READ | PROT_WRITE, MAP_SHARED, fd, Int(offset)),
            data != MAP_FAILED
        else {
            throw VideoError.unsupportedOperation(message: "Failed to map buffer")
        }

        defer { munmap(data, Int(length)) }

        // Stop streaming
        result = ioctl(fd, UInt(VIDIOC_STREAMOFF), &type)
        if result < 0 {
            throw VideoError.ioctlFailed(command: "VIDIOC_STREAMOFF", errno: errno)
        }

        // Copy the frame data
        let frameData = Data(bytes: data, count: Int(length))
        return frameData
    }

    /// Capture a frame and convert it to RGB format
    /// - Parameters:
    ///   - width: The desired frame width
    ///   - height: The desired frame height
    /// - Returns: RGB frame data (3 bytes per pixel)
    public func captureRGBFrame(width: UInt32 = 640, height: UInt32 = 480) throws -> Data {
        let yuyvData = try captureFrame(width: width, height: height)
        return VideoFormatConverter.yuyvToRGB(
            yuyvData: yuyvData,
            width: Int(width),
            height: Int(height)
        )
    }
}

/// Errors that can occur when working with V4L2 devices
public enum VideoError: Error {
    case deviceOpenFailed(path: String, errno: Int32)
    case ioctlFailed(command: String, errno: Int32)
    case unsupportedOperation(message: String)
    case invalidData(message: String)
}

/// A class providing access to V4L2 video devices
public class VideoDeviceManager {

    /// List all available video devices in the system
    /// - Returns: An array of VideoDevice objects
    public static func listDevices() throws -> [VideoDevice] {
        var devices: [VideoDevice] = []

        // Look for devices in /dev
        let fileManager = FileManager.default
        let devDir = "/dev"

        do {
            let contents = try fileManager.contentsOfDirectory(atPath: devDir)

            // Filter for video device nodes (video0, video1, etc.)
            let videoDevices = contents.filter {
                $0.hasPrefix("video")
                    && CharacterSet.decimalDigits.contains(
                        $0.last?.unicodeScalars.first ?? Unicode.Scalar(0)
                    )
            }

            // Try to open each device and query its capabilities
            for deviceName in videoDevices {
                let devicePath = "\(devDir)/\(deviceName)"

                do {
                    let device = try queryDeviceInfo(path: devicePath)
                    devices.append(device)
                } catch {
                    print("Warning: Could not query device \(devicePath): \(error)")
                    // Continue to the next device
                }
            }

        } catch {
            throw VideoError.unsupportedOperation(
                message: "Could not list contents of /dev: \(error)"
            )
        }

        return devices
    }

    /// Print information about all available video devices to the console
    public static func printDeviceList() {
        do {
            let devices = try listDevices()

            if devices.isEmpty {
                print("No video devices found.")
                return
            }

            print("Available video devices:")
            print("------------------------")

            for (index, device) in devices.enumerated() {
                print("[\(index)] \(device.path):")
                print("    Name: \(device.name)")
                print("    Driver: \(device.driver)")
                print("    Bus info: \(device.busInfo)")
                print("    Capabilities: \(String(format: "0x%08X", device.capabilities))")
                print("    Supports capture: \(device.supportsCapture ? "Yes" : "No")")
                print("    Supports streaming: \(device.supportsStreaming ? "Yes" : "No")")
                print("")
            }
        } catch {
            print("Error listing video devices: \(error)")
        }
    }

    // MARK: - Private Methods

    /// Query a device's information
    /// - Parameter path: The path to the video device
    /// - Returns: A VideoDevice object with the device's information
    private static func queryDeviceInfo(path: String) throws -> VideoDevice {
        // Open the device
        let fd = open(path, O_RDWR)
        if fd < 0 {
            throw VideoError.deviceOpenFailed(path: path, errno: errno)
        }
        defer { close(fd) }

        // Query device capabilities using a manual approach since the v4l2_capability struct may not be exposed correctly
        // Create a buffer large enough to hold v4l2_capability (typically 104 bytes)
        var capabilityBuffer = [UInt8](repeating: 0, count: 104)

        let result = ioctl(fd, UInt(VIDIOC_QUERYCAP), &capabilityBuffer)

        if result < 0 {
            throw VideoError.ioctlFailed(command: "VIDIOC_QUERYCAP", errno: errno)
        }

        // Extract information from the buffer
        // The layout of v4l2_capability is:
        // - driver: char[16]
        // - card: char[32]
        // - bus_info: char[32]
        // - version: __u32
        // - capabilities: __u32
        // - device_caps: __u32
        // - reserved: __u32[3]

        // Extract strings - this assumes null-terminated C strings
        let driverData = capabilityBuffer[0..<16]
        let nameData = capabilityBuffer[16..<48]
        let busInfoData = capabilityBuffer[48..<80]

        // Extract capabilities (4 bytes at offset 84)
        let capabilities = capabilityBuffer[84..<88].withUnsafeBytes { $0.load(as: UInt32.self) }

        // Convert data to strings
        let driver = String(bytes: driverData.filter { $0 != 0 }, encoding: .utf8) ?? "Unknown"
        let name = String(bytes: nameData.filter { $0 != 0 }, encoding: .utf8) ?? "Unknown"
        let busInfo = String(bytes: busInfoData.filter { $0 != 0 }, encoding: .utf8) ?? "Unknown"

        return VideoDevice(
            path: path,
            name: name,
            driver: driver,
            busInfo: busInfo,
            capabilities: capabilities
        )
    }
}

/// Utility functions for video format conversion
public struct VideoFormatConverter {
    /// Convert YUV values to RGB
    /// - Parameters:
    ///   - y: Luminance value
    ///   - u: U chrominance value
    ///   - v: V chrominance value
    /// - Returns: Tuple containing (red, green, blue) values
    private static func yuvToRGB(y: Int32, u: Int32, v: Int32) -> (r: UInt8, g: UInt8, b: UInt8) {
        // Convert YUV to RGB using BT.601 conversion
        // First, adjust ranges
        let y1 = y
        let u1 = u - 128
        let v1 = v - 128

        // Calculate RGB values
        var r = (y1 + (359 * v1) / 256)
        var g = (y1 - (88 * u1) / 256 - (183 * v1) / 256)
        var b = (y1 + (454 * u1) / 256)

        // Clamp values to 0-255
        r = max(0, min(255, r))
        g = max(0, min(255, g))
        b = max(0, min(255, b))

        return (UInt8(r), UInt8(g), UInt8(b))
    }

    /// Convert YUYV frame data to RGB
    /// - Parameters:
    ///   - yuyvData: Raw YUYV frame data
    ///   - width: Frame width
    ///   - height: Frame height
    /// - Returns: RGB data (3 bytes per pixel)
    public static func yuyvToRGB(yuyvData: Data, width: Int, height: Int) -> Data {
        var rgbData = Data(count: width * height * 3)  // 3 bytes per pixel (RGB)

        // Process two pixels at a time (4 bytes YUYV -> 6 bytes RGB)
        for y in 0..<height {
            for x in stride(from: 0, to: width, by: 2) {
                let index = (y * width + x) * 2
                guard index + 3 < yuyvData.count else { break }

                // Extract YUYV values
                let y1 = Int32(yuyvData[index])
                let u = Int32(yuyvData[index + 1])
                let y2 = Int32(yuyvData[index + 2])
                let v = Int32(yuyvData[index + 3])

                // Convert first pixel
                let rgb1 = yuvToRGB(y: y1, u: u, v: v)
                let rgbIndex1 = (y * width + x) * 3
                rgbData[rgbIndex1] = rgb1.r
                rgbData[rgbIndex1 + 1] = rgb1.g
                rgbData[rgbIndex1 + 2] = rgb1.b

                // Convert second pixel if within bounds
                if x + 1 < width {
                    let rgb2 = yuvToRGB(y: y2, u: u, v: v)
                    let rgbIndex2 = (y * width + x + 1) * 3
                    rgbData[rgbIndex2] = rgb2.r
                    rgbData[rgbIndex2 + 1] = rgb2.g
                    rgbData[rgbIndex2 + 2] = rgb2.b
                }
            }
        }

        return rgbData
    }
}
