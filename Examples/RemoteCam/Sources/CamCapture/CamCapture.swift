// CamCapture: minimal V4L2 camera capture, trimmed from Examples/HelloVideo's
// LinuxVideo module.
//
// HARDWARE-UNVERIFIED: this file has only ever been type-checked (it cannot
// compile on macOS at all, since CLinuxVideo requires <linux/videodev2.h>,
// and there is no V4L2-capable Linux box with a webcam available in this
// environment). The ioctl sequence below is copied verbatim from
// Examples/HelloVideo/Sources/LinuxVideo/LinuxVideo.swift, which is the one
// piece of prior art in this repo that has actually driven a V4L2 device, so
// this reuses its exact call order (S_FMT -> REQBUFS -> QUERYBUF -> QBUF ->
// STREAMON -> mmap -> DQBUF -> STREAMOFF) rather than inventing a new one.
//
// Unlike LinuxVideo, this module returns raw RGB bytes ([UInt8], 3
// bytes/pixel) instead of JPEG.RGB values — RemoteCam's wire protocol sends
// uncompressed RGB, so there is no need for the swift-jpeg dependency here.

import CLinuxVideo
import Foundation

// V4L2 capability constants (values from linux/videodev2.h).
private let V4L2_CAP_VIDEO_CAPTURE: UInt32 = 0x0000_0001

// V4L2 buffer types / memory types / pixel formats.
private let V4L2_BUF_TYPE_VIDEO_CAPTURE: UInt32 = 1
private let V4L2_MEMORY_MMAP: UInt32 = 1
private let V4L2_PIX_FMT_YUYV: UInt32 = 0x5659_5559  // 'YUYV'

/// Errors that can occur while driving a V4L2 device.
public enum CamCaptureError: Error, CustomStringConvertible {
    case noCaptureDeviceFound
    case deviceOpenFailed(path: String, errno: Int32)
    case ioctlFailed(command: String, errno: Int32)
    case unsupportedOperation(message: String)
    case invalidData(message: String)

    public var description: String {
        switch self {
        case .noCaptureDeviceFound:
            return "no V4L2 capture device found under /dev"
        case .deviceOpenFailed(let path, let errno):
            return "failed to open \(path) (errno \(errno))"
        case .ioctlFailed(let command, let errno):
            return "\(command) failed (errno \(errno))"
        case .unsupportedOperation(let message):
            return message
        case .invalidData(let message):
            return message
        }
    }
}

/// A V4L2 video capture device, identified by its `/dev/videoN` path.
public struct VideoDevice {
    /// The device path (e.g. "/dev/video0").
    public let path: String

    public init(path: String) {
        self.path = path
    }

    /// Find the first `/dev/video*` node that reports video-capture capability.
    ///
    /// HARDWARE-UNVERIFIED: mirrors HelloVideo's `VideoDeviceManager.listDevices`
    /// capability probing (a raw VIDIOC_QUERYCAP into a manually laid-out
    /// buffer), trimmed to stop at the first usable device instead of building
    /// a full list.
    public static func firstCaptureDevice() throws -> VideoDevice {
        let fileManager = FileManager.default
        let devDir = "/dev"

        let contents: [String]
        do {
            contents = try fileManager.contentsOfDirectory(atPath: devDir)
        } catch {
            throw CamCaptureError.unsupportedOperation(
                message: "could not list contents of \(devDir): \(error)"
            )
        }

        let candidates = contents.filter {
            $0.hasPrefix("video")
                && CharacterSet.decimalDigits.contains(
                    $0.last?.unicodeScalars.first ?? Unicode.Scalar(0)
                )
        }
        .sorted()

        for name in candidates {
            let path = "\(devDir)/\(name)"
            if let capabilities = try? queryCapabilities(path: path),
                (capabilities & V4L2_CAP_VIDEO_CAPTURE) != 0
            {
                return VideoDevice(path: path)
            }
        }

        throw CamCaptureError.noCaptureDeviceFound
    }

    private static func queryCapabilities(path: String) throws -> UInt32 {
        let fd = open(path, O_RDWR)
        if fd < 0 {
            throw CamCaptureError.deviceOpenFailed(path: path, errno: errno)
        }
        defer { close(fd) }

        // v4l2_capability is 104 bytes; capabilities is a __u32 at offset 84.
        var capabilityBuffer = [UInt8](repeating: 0, count: 104)
        let result = ioctl(fd, UInt(WENDY_VIDIOC_QUERYCAP), &capabilityBuffer)
        if result < 0 {
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_QUERYCAP", errno: errno)
        }

        return capabilityBuffer[84..<88].withUnsafeBytes { $0.load(as: UInt32.self) }
    }

    /// Capture a single frame as raw YUYV bytes.
    ///
    /// Opens the device, negotiates the format, requests/maps a single mmap
    /// buffer, streams on just long enough to dequeue one frame, then streams
    /// off and closes the fd again — so the device is never held open between
    /// calls. This mirrors `LinuxVideo.captureFrame` in HelloVideo exactly
    /// (same ioctl order), which is also why this is comparatively expensive
    /// per frame (full setup/teardown each time); at the ~2fps this example
    /// targets that's an acceptable, and deliberately simple, tradeoff — it
    /// also means "stop streaming" or "client disconnected" never needs an
    /// explicit device-release step, because the device is only ever open for
    /// the duration of a single captureFrame() call.
    public func captureFrame(width: UInt32, height: UInt32) throws -> Data {
        let fd = open(path, O_RDWR)
        if fd < 0 {
            throw CamCaptureError.deviceOpenFailed(path: path, errno: errno)
        }
        defer { close(fd) }

        var format = v4l2_format()
        format.type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        format.fmt.pix.width = width
        format.fmt.pix.height = height
        format.fmt.pix.pixelformat = V4L2_PIX_FMT_YUYV
        format.fmt.pix.field = V4L2_FIELD_NONE.rawValue
        format.fmt.pix.bytesperline = width * 2
        format.fmt.pix.sizeimage = width * height * 2

        var result = ioctl(fd, UInt(WENDY_VIDIOC_S_FMT), &format)
        if result < 0 {
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_S_FMT", errno: errno)
        }

        var reqbuf = v4l2_requestbuffers()
        reqbuf.count = 1
        reqbuf.memory = V4L2_MEMORY_MMAP
        reqbuf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE

        result = ioctl(fd, UInt(WENDY_VIDIOC_REQBUFS), &reqbuf)
        if result < 0 {
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_REQBUFS", errno: errno)
        }

        var buffer = v4l2_buffer()
        buffer.type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        buffer.memory = V4L2_MEMORY_MMAP
        buffer.index = 0
        buffer.length = width * height * 2
        result = ioctl(fd, UInt(WENDY_VIDIOC_QUERYBUF), &buffer)
        if result < 0 {
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_QUERYBUF", errno: errno)
        }

        buffer = v4l2_buffer()
        buffer.memory = V4L2_MEMORY_MMAP
        buffer.type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        buffer.index = 0

        result = ioctl(fd, UInt(WENDY_VIDIOC_QBUF), &buffer)
        if result < 0 {
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_QBUF", errno: errno)
        }

        var type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        result = ioctl(fd, UInt(WENDY_VIDIOC_STREAMON), &type)
        if result < 0 {
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_STREAMON", errno: errno)
        }

        guard
            let mapped = mmap(
                nil,
                Int(buffer.length),
                PROT_READ | PROT_WRITE,
                MAP_SHARED,
                fd,
                Int(buffer.m.offset)
            ),
            mapped != MAP_FAILED
        else {
            _ = ioctl(fd, UInt(WENDY_VIDIOC_STREAMOFF), &type)
            throw CamCaptureError.unsupportedOperation(message: "failed to mmap V4L2 buffer")
        }
        defer { munmap(mapped, Int(buffer.length)) }

        var dqbuf = v4l2_buffer()
        dqbuf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        dqbuf.memory = V4L2_MEMORY_MMAP
        result = ioctl(fd, UInt(WENDY_VIDIOC_DQBUF), &dqbuf)
        if result < 0 {
            _ = ioctl(fd, UInt(WENDY_VIDIOC_STREAMOFF), &type)
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_DQBUF", errno: errno)
        }

        let imageData = Data(bytes: mapped, count: Int(buffer.length))

        result = ioctl(fd, UInt(WENDY_VIDIOC_STREAMOFF), &type)
        if result < 0 {
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_STREAMOFF", errno: errno)
        }

        return imageData
    }
}

/// A persistent V4L2 capture stream: negotiates the format and starts
/// streaming once, then repeatedly dequeues/requeues from a small ring of
/// mmap'd buffers. This is the fix for the frame-rate ceiling the original
/// per-frame `VideoDevice.captureFrame` design had — that method opened the
/// device, renegotiated the format, allocated buffers, and did a fresh
/// STREAMON/STREAMOFF cycle on *every single frame*. STREAMON is a real
/// hardware operation for USB Video Class devices: the driver has to
/// (re-)start the endpoint and the first DQBUF after it often blocks for a
/// full frame interval or more while the sensor spins up, so paying that
/// cost per frame silently capped throughput far below what continuous
/// streaming can do. Keeping the stream on and using a multi-buffer ring
/// (so the driver can be filling one buffer while a previous one is still
/// being read out) is the standard V4L2 capture pattern.
public final class CaptureSession {
    private let fd: Int32
    private let buffers: [(pointer: UnsafeMutableRawPointer, length: Int)]
    private var streaming = true

    /// The format the driver actually negotiated — not necessarily what was
    /// requested. VIDIOC_S_FMT is a negotiation, not a command: a driver
    /// that doesn't support the exact requested width/height substitutes the
    /// closest mode it does support and reports it back in the same struct.
    /// Callers MUST use these (not the width/height they originally asked
    /// for) when interpreting captured bytes or filling in a frame header —
    /// using the request instead of the negotiated result would silently
    /// mismatch the wire protocol's declared dimensions against the actual
    /// pixel data the moment a resolution isn't exactly supported.
    public let width: UInt32
    public let height: UInt32

    /// Opens `device`, negotiates YUYV at `width`x`height` (see the `width`/
    /// `height` properties for why the driver may adjust this), allocates
    /// and queues `bufferCount` mmap'd buffers, and starts streaming. Throws
    /// (leaving nothing open) if any step fails.
    public init(device: VideoDevice, width: UInt32, height: UInt32, bufferCount: Int = 4) throws {
        let fd = open(device.path, O_RDWR)
        guard fd >= 0 else {
            throw CamCaptureError.deviceOpenFailed(path: device.path, errno: errno)
        }

        var format = v4l2_format()
        format.type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        format.fmt.pix.width = width
        format.fmt.pix.height = height
        format.fmt.pix.pixelformat = V4L2_PIX_FMT_YUYV
        format.fmt.pix.field = V4L2_FIELD_NONE.rawValue
        format.fmt.pix.bytesperline = width * 2
        format.fmt.pix.sizeimage = width * height * 2
        guard ioctl(fd, UInt(WENDY_VIDIOC_S_FMT), &format) >= 0 else {
            let e = errno
            close(fd)
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_S_FMT", errno: e)
        }
        // format.fmt.pix.{width,height} now hold what the driver actually
        // set, which S_FMT overwrites in place — read them back rather than
        // trusting the request.
        self.width = format.fmt.pix.width
        self.height = format.fmt.pix.height

        var reqbuf = v4l2_requestbuffers()
        reqbuf.count = UInt32(bufferCount)
        reqbuf.memory = V4L2_MEMORY_MMAP
        reqbuf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        guard ioctl(fd, UInt(WENDY_VIDIOC_REQBUFS), &reqbuf) >= 0 else {
            let e = errno
            close(fd)
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_REQBUFS", errno: e)
        }
        guard reqbuf.count > 0 else {
            close(fd)
            throw CamCaptureError.unsupportedOperation(message: "driver allocated zero capture buffers")
        }

        var mapped: [(pointer: UnsafeMutableRawPointer, length: Int)] = []
        for index in 0..<reqbuf.count {
            var buffer = v4l2_buffer()
            buffer.type = V4L2_BUF_TYPE_VIDEO_CAPTURE
            buffer.memory = V4L2_MEMORY_MMAP
            buffer.index = index
            guard ioctl(fd, UInt(WENDY_VIDIOC_QUERYBUF), &buffer) >= 0 else {
                let e = errno
                for m in mapped { munmap(m.pointer, m.length) }
                close(fd)
                throw CamCaptureError.ioctlFailed(command: "VIDIOC_QUERYBUF", errno: e)
            }
            guard
                let ptr = mmap(nil, Int(buffer.length), PROT_READ | PROT_WRITE, MAP_SHARED, fd, Int(buffer.m.offset)),
                ptr != MAP_FAILED
            else {
                for m in mapped { munmap(m.pointer, m.length) }
                close(fd)
                throw CamCaptureError.unsupportedOperation(message: "failed to mmap V4L2 buffer \(index)")
            }
            mapped.append((ptr, Int(buffer.length)))

            var qbuf = v4l2_buffer()
            qbuf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE
            qbuf.memory = V4L2_MEMORY_MMAP
            qbuf.index = index
            guard ioctl(fd, UInt(WENDY_VIDIOC_QBUF), &qbuf) >= 0 else {
                let e = errno
                for m in mapped { munmap(m.pointer, m.length) }
                close(fd)
                throw CamCaptureError.ioctlFailed(command: "VIDIOC_QBUF", errno: e)
            }
        }

        var type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        guard ioctl(fd, UInt(WENDY_VIDIOC_STREAMON), &type) >= 0 else {
            let e = errno
            for m in mapped { munmap(m.pointer, m.length) }
            close(fd)
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_STREAMON", errno: e)
        }

        self.fd = fd
        self.buffers = mapped
    }

    /// Blocks until the next frame is available, copies it out, and
    /// immediately re-queues the buffer so the driver can keep filling it —
    /// no STREAMON/STREAMOFF round trip, unlike the original per-frame path.
    public func captureFrame() throws -> Data {
        var dqbuf = v4l2_buffer()
        dqbuf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        dqbuf.memory = V4L2_MEMORY_MMAP
        guard ioctl(fd, UInt(WENDY_VIDIOC_DQBUF), &dqbuf) >= 0 else {
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_DQBUF", errno: errno)
        }
        let slot = buffers[Int(dqbuf.index)]
        let byteCount = dqbuf.bytesused > 0 ? Int(dqbuf.bytesused) : slot.length
        let data = Data(bytes: slot.pointer, count: byteCount)

        var qbuf = v4l2_buffer()
        qbuf.type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        qbuf.memory = V4L2_MEMORY_MMAP
        qbuf.index = dqbuf.index
        guard ioctl(fd, UInt(WENDY_VIDIOC_QBUF), &qbuf) >= 0 else {
            throw CamCaptureError.ioctlFailed(command: "VIDIOC_QBUF (re-queue)", errno: errno)
        }

        return data
    }

    /// Stops streaming and unmaps every buffer. Safe to call more than once;
    /// also runs automatically from `deinit` as a backstop. Named `stop`
    /// rather than `close` so calls to the POSIX `close(fd)` above resolve
    /// unambiguously against the free function, not this method.
    public func stop() {
        guard streaming else { return }
        streaming = false
        var type = V4L2_BUF_TYPE_VIDEO_CAPTURE
        _ = ioctl(fd, UInt(WENDY_VIDIOC_STREAMOFF), &type)
        for buffer in buffers {
            munmap(buffer.pointer, buffer.length)
        }
        close(fd)
    }

    deinit {
        stop()
    }
}

/// Convert one YUYV-packed pixel pair (4 bytes: Y0 U Y1 V) to two RGB pixels
/// using BT.601 conversion — same formula as HelloVideo's `yuvToRGB`.
private func yuvToRGB(y: UInt8, u: UInt8, v: UInt8) -> (r: UInt8, g: UInt8, b: UInt8) {
    let y1 = Int32(y)
    let u1 = Int32(u) - 128
    let v1 = Int32(v) - 128

    var r = y1 + (359 * v1) / 256
    var g = y1 - (88 * u1) / 256 - (183 * v1) / 256
    var b = y1 + (454 * u1) / 256

    r = max(0, min(255, r))
    g = max(0, min(255, g))
    b = max(0, min(255, b))

    return (UInt8(r), UInt8(g), UInt8(b))
}

/// Convert a full YUYV frame to flat, row-major RGB bytes (3 bytes/pixel: R,
/// G, B, R, G, B, ...) — the exact layout RemoteCam's `FRAME_RGB` wire
/// payload expects. This is the raw-bytes sibling of HelloVideo's
/// `yuyvSRGBToRGB` (which instead builds `[JPEG.RGB]` for JPEG encoding).
public func yuyvToRGBBytes(_ yuyvData: Data, width: Int, height: Int) throws -> [UInt8] {
    guard width % 2 == 0 else {
        throw CamCaptureError.invalidData(message: "width must be even for YUYV decoding")
    }
    guard yuyvData.count >= width * height * 2 else {
        throw CamCaptureError.invalidData(
            message: "YUYV buffer too small: expected at least \(width * height * 2) bytes, got \(yuyvData.count)"
        )
    }

    var rgb = [UInt8](repeating: 0, count: width * height * 3)
    let bytes = [UInt8](yuyvData)

    for y in 0..<height {
        var x = 0
        while x < width {
            let index = (y * width + x) * 2
            guard index + 3 < bytes.count else { break }

            let y0 = bytes[index]
            let u = bytes[index + 1]
            let y1 = bytes[index + 2]
            let v = bytes[index + 3]

            let px0 = yuvToRGB(y: y0, u: u, v: v)
            let outIndex0 = (y * width + x) * 3
            rgb[outIndex0] = px0.r
            rgb[outIndex0 + 1] = px0.g
            rgb[outIndex0 + 2] = px0.b

            if x + 1 < width {
                let px1 = yuvToRGB(y: y1, u: u, v: v)
                let outIndex1 = (y * width + x + 1) * 3
                rgb[outIndex1] = px1.r
                rgb[outIndex1 + 1] = px1.g
                rgb[outIndex1 + 2] = px1.b
            }

            x += 2
        }
    }

    return rgb
}
