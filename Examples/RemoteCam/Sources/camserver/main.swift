// camserver — RemoteCam's "Device B": captures the local webcam and streams
// it to a single connected peer over the RemoteCam wire protocol (see
// ../../README.md and the protocol spec it links). Networking (RemoteCamWire)
// is plain POSIX sockets and builds on macOS or Linux; camera capture
// (CamCapture) is Linux/V4L2-only and HARDWARE-UNVERIFIED — see CamCapture.swift.

import CamCapture
import CLinuxVideo
import Foundation
import RemoteCamWire

#if canImport(Glibc)
    import Glibc
#elseif canImport(Darwin)
    import Darwin
#endif

// Fixed demo parameters (see protocol spec — no negotiation).
let port: UInt16 = 9090
// Requested resolution — CaptureSession.width/height (not these) is what
// actually gets used for conversion/encoding, since the driver may
// substitute a mode it supports instead of this exact one (see
// CaptureSession's doc comment). 640x480 fills the viewer's ~1840x890
// video panel far better than the original 320x240 without pushing
// per-frame bandwidth (640x480x3 = ~900KB) or the YUYV->RGB conversion's
// per-pixel Swift loop past what a LAN-direct mesh link and a Pi absorb
// comfortably at the ~10fps ceiling below.
let requestedCaptureWidth: UInt32 = 640
let requestedCaptureHeight: UInt32 = 480
// Soft cap, not the camera's actual rate: CaptureSession keeps the V4L2
// stream continuously on, so DQBUF already paces to whatever the sensor
// delivers (often 15-30fps) — this sleep just bounds bandwidth/CPU for the
// demo's uncompressed-RGB wire format (320x240x3 bytes/frame) rather than
// sending as fast as the camera can produce. The original 0.5s (~2fps) cap
// was inherited from a design that re-opened and STREAMON/STREAMOFF'd the
// V4L2 device on every frame (see CaptureSession's doc comment) — that
// per-frame hardware restart was almost certainly the actual bottleneck,
// not this sleep, so lowering it alone couldn't have gone much above ~2fps
// before the session became persistent.
let frameInterval: TimeInterval = 0.1  // ~10fps ceiling

// Unbuffered stdout so `wendy device logs` / `wendy run` see log lines as
// they happen rather than only on a buffer flush. Done in C
// (wendy_camserver_unbuffer_stdout, in CLinuxVideo.h) because Swift 6 flags
// any Swift-level reference to the C global `stdout` as concurrency-unsafe.
wendy_camserver_unbuffer_stdout()

func log(_ message: String) {
    print("[camserver] \(message)")
}

/// Guards against a second concurrent connection: camserver serves one
/// client at a time (demo scope, per the protocol spec). A connection
/// attempt that arrives while another is active is rejected/closed
/// immediately rather than queued.
final class ConnectionGate: @unchecked Sendable {
    private let lock = NSLock()
    private var busy = false

    func tryAcquire() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if busy { return false }
        busy = true
        return true
    }

    func release() {
        lock.lock()
        defer { lock.unlock() }
        busy = false
    }
}

/// Per-connection state shared between the command-reader loop and the
/// frame-streaming loop, both of which run concurrently against the same
/// socket. That's safe here because one side only ever reads and the other
/// only ever writes — POSIX sockets treat the two directions independently.
final class ConnectionState: @unchecked Sendable {
    private let lock = NSLock()
    private var _streaming = false
    private var _closed = false

    var streaming: Bool {
        lock.lock()
        defer { lock.unlock() }
        return _streaming
    }

    var closed: Bool {
        lock.lock()
        defer { lock.unlock() }
        return _closed
    }

    func setStreaming(_ value: Bool) {
        lock.lock()
        _streaming = value
        lock.unlock()
    }

    func markClosed() {
        lock.lock()
        _streaming = false
        _closed = true
        lock.unlock()
    }
}

/// Sleep for `seconds`, but wake up early (checking every 25ms) if the
/// connection is torn down mid-sleep, so CMD_STOP / disconnect take effect
/// promptly instead of after up to a full frame interval.
func sleepUnlessClosed(_ state: ConnectionState, seconds: TimeInterval) {
    let slice: TimeInterval = 0.025
    var elapsed: TimeInterval = 0
    while elapsed < seconds {
        if state.closed { return }
        Thread.sleep(forTimeInterval: min(slice, seconds - elapsed))
        elapsed += slice
    }
}

/// Capture+stream loop: while `state.streaming` is true, grabs a frame,
/// converts it to RGB, and sends it as a FRAME_RGB wire frame at
/// `frameInterval` cadence. Runs on its own thread for the connection's
/// lifetime so CMD_START/CMD_STOP can flip streaming on/off without blocking
/// (or being blocked by) the command-reader loop.
///
/// The V4L2 device is opened once per CMD_START (via `CaptureSession`,
/// which keeps the stream continuously on) rather than once per frame —
/// see CaptureSession's doc comment for why the original per-frame
/// open/STREAMON/STREAMOFF design capped throughput far below what the
/// camera can actually deliver. The session is torn down on CMD_STOP and on
/// disconnect, so "streaming paused" still fully releases the camera
/// between starts, matching the original's behavior.
func streamFrames(channel: SocketChannel, state: ConnectionState) {
    var session: CaptureSession?
    defer { session?.stop() }

    while !state.closed {
        guard state.streaming else {
            if let s = session {
                s.stop()
                session = nil
            }
            sleepUnlessClosed(state, seconds: 0.05)
            continue
        }

        do {
            let activeSession: CaptureSession
            if let s = session {
                activeSession = s
            } else {
                let device = try VideoDevice.firstCaptureDevice()
                activeSession = try CaptureSession(
                    device: device, width: requestedCaptureWidth, height: requestedCaptureHeight)
                session = activeSession
                log(
                    "capture session opened on \(device.path) at \(activeSession.width)x\(activeSession.height)"
                        + (activeSession.width != requestedCaptureWidth || activeSession.height != requestedCaptureHeight
                            ? " (requested \(requestedCaptureWidth)x\(requestedCaptureHeight), driver substituted this)"
                            : ""))
            }

            let frameStart = Date()
            let yuyv = try activeSession.captureFrame()
            let captureEnd = Date()
            let rgb = try yuyvToRGBBytes(
                yuyv, width: Int(activeSession.width), height: Int(activeSession.height))
            let convertEnd = Date()
            let payload = encodeFrameRGBPayload(
                width: UInt16(activeSession.width), height: UInt16(activeSession.height), rgb: rgb)
            try writeFrame(WireFrame(type: .frameRGB, payload: payload), to: channel)
            let sendEnd = Date()
            log(
                "frame timing: capture=\(Int(captureEnd.timeIntervalSince(frameStart) * 1000))ms "
                    + "convert=\(Int(convertEnd.timeIntervalSince(captureEnd) * 1000))ms "
                    + "send=\(Int(sendEnd.timeIntervalSince(convertEnd) * 1000))ms")
        } catch {
            log("capture/send failed, stopping stream: \(error)")
            session?.stop()
            session = nil
            state.markClosed()
            channel.close()
            return
        }

        sleepUnlessClosed(state, seconds: frameInterval)
    }
}

/// Reads CMD_START / CMD_STOP frames until the client disconnects. Any other
/// frame type on this direction of the wire is a fatal protocol error: sends
/// ERR and returns (closing the connection).
func readCommands(channel: SocketChannel, state: ConnectionState) {
    while true {
        let frame: WireFrame
        do {
            frame = try readFrame(from: channel)
        } catch {
            log("client disconnected: \(error)")
            return
        }

        switch frame.type {
        case .cmdStart:
            log("CMD_START received; streaming at ~2fps, 320x240")
            state.setStreaming(true)
        case .cmdStop:
            log("CMD_STOP received; streaming paused")
            state.setStreaming(false)
        case .frameRGB, .err:
            log("unexpected frame type \(frame.type.rawValue) from client; sending ERR and closing")
            let message = Array("camserver only accepts CMD_START/CMD_STOP".utf8)
            try? writeFrame(WireFrame(type: .err, payload: message), to: channel)
            return
        }
    }
}

func handleConnection(channel: SocketChannel, gate: ConnectionGate) {
    log("client connected")
    let state = ConnectionState()

    let streamingThread = Thread { streamFrames(channel: channel, state: state) }
    streamingThread.start()

    readCommands(channel: channel, state: state)

    state.markClosed()
    channel.close()
    gate.release()
    log("connection closed; camera released")
}

log("RemoteCam camserver starting (V4L2 capture path is HARDWARE-UNVERIFIED — see README)")

let listener: TCPListener
do {
    listener = try TCPListener(port: port)
} catch {
    log("failed to listen on port \(port): \(error)")
    exit(1)
}

log("listening on 0.0.0.0:\(port)")

let gate = ConnectionGate()

while true {
    let channel: SocketChannel
    do {
        channel = try listener.acceptOne()
    } catch {
        log("accept failed: \(error)")
        continue
    }

    guard gate.tryAcquire() else {
        log("rejecting second concurrent connection (camserver serves one client at a time)")
        channel.close()
        continue
    }

    Thread.detachNewThread {
        handleConnection(channel: channel, gate: gate)
    }
}
