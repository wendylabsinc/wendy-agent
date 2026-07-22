// RemoteCam wire protocol — see the spec this was built against:
//
//   Frame = [1-byte type][4-byte big-endian uint32 length][payload]
//     0x01 CMD_START  (client -> server, empty payload)
//     0x02 CMD_STOP   (client -> server, empty payload)
//     0x10 FRAME_RGB  (server -> client, payload = [uint16 width][uint16
//                      height][width*height*3 bytes RGB, row-major])
//     0x7F ERR        (either direction, UTF-8 message payload, then close)
//
// This file implements the framing generically (any FrameType, arbitrary
// payload) plus the one payload codec camserver actually needs on the wire:
// packing/unpacking the FRAME_RGB width/height header. Decoding an inbound
// FRAME_RGB isn't needed here (camserver never receives one), so it isn't
// implemented — Device A's viewer owns that side.

/// The four frame types defined by the RemoteCam wire protocol.
public enum FrameType: UInt8, Sendable {
    case cmdStart = 0x01
    case cmdStop = 0x02
    case frameRGB = 0x10
    case err = 0x7F
}

/// A decoded (or to-be-encoded) wire frame.
public struct WireFrame {
    public let type: FrameType
    public let payload: [UInt8]

    public init(type: FrameType, payload: [UInt8] = []) {
        self.type = type
        self.payload = payload
    }
}

/// Errors specific to decoding the wire protocol (as opposed to the raw
/// socket I/O errors in `SocketError`).
public enum WireProtocolError: Error, CustomStringConvertible {
    case unknownFrameType(UInt8)
    case frameTooLarge(UInt32)

    public var description: String {
        switch self {
        case .unknownFrameType(let raw):
            let hex = String(raw, radix: 16, uppercase: true)
            return "unknown frame type 0x\(hex.count < 2 ? "0" + hex : hex)"
        case .frameTooLarge(let length):
            return "frame payload too large: \(length) bytes"
        }
    }
}

/// Cap on an accepted payload length, purely as a sanity guard against a
/// corrupt/malicious length field turning into an unbounded allocation. Well
/// above the largest legitimate FRAME_RGB payload this app ever sends
/// (320*240*3 + 4 = 230,404 bytes).
private let maxFramePayloadBytes: UInt32 = 16 * 1024 * 1024

/// Read one complete frame from `channel`, blocking until it arrives.
///
/// Throws `SocketError.connectionClosed` if the peer disconnects (whether
/// cleanly or mid-frame), or `WireProtocolError.unknownFrameType` /
/// `.frameTooLarge` if the header is well-formed at the byte level but
/// violates the protocol.
public func readFrame(from channel: ByteChannel) throws -> WireFrame {
    let header = try channel.readExactly(5)
    let rawType = header[0]
    guard let type = FrameType(rawValue: rawType) else {
        throw WireProtocolError.unknownFrameType(rawType)
    }

    let length =
        UInt32(header[1]) << 24 | UInt32(header[2]) << 16 | UInt32(header[3]) << 8
        | UInt32(header[4])
    guard length <= maxFramePayloadBytes else {
        throw WireProtocolError.frameTooLarge(length)
    }

    let payload = length > 0 ? try channel.readExactly(Int(length)) : []
    return WireFrame(type: type, payload: payload)
}

/// Write one complete frame to `channel`.
public func writeFrame(_ frame: WireFrame, to channel: ByteChannel) throws {
    var header = [UInt8]()
    header.reserveCapacity(5)
    header.append(frame.type.rawValue)
    let length = UInt32(frame.payload.count)
    header.append(UInt8((length >> 24) & 0xFF))
    header.append(UInt8((length >> 16) & 0xFF))
    header.append(UInt8((length >> 8) & 0xFF))
    header.append(UInt8(length & 0xFF))

    try channel.writeAll(header)
    if !frame.payload.isEmpty {
        try channel.writeAll(frame.payload)
    }
}

/// Pack a FRAME_RGB payload: `[uint16 width][uint16 height][width*height*3
/// bytes RGB]`, big-endian, row-major, 3 bytes/pixel.
///
/// - Precondition: `rgb.count == Int(width) * Int(height) * 3`.
public func encodeFrameRGBPayload(width: UInt16, height: UInt16, rgb: [UInt8]) -> [UInt8] {
    precondition(
        rgb.count == Int(width) * Int(height) * 3,
        "RGB buffer size \(rgb.count) does not match \(width)x\(height)x3")

    var payload = [UInt8]()
    payload.reserveCapacity(4 + rgb.count)
    payload.append(UInt8((width >> 8) & 0xFF))
    payload.append(UInt8(width & 0xFF))
    payload.append(UInt8((height >> 8) & 0xFF))
    payload.append(UInt8(height & 0xFF))
    payload.append(contentsOf: rgb)
    return payload
}
