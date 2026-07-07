// Package foxglovebridge is the length-framed binary control protocol spoken
// between the agent and the compiled wendy-ros2-bridge process. Frame layout:
//
//	[uint32 LE total_len][uint8 tag][payload]   total_len counts tag+payload
//
// Strings inside a payload are [uint16 LE len][bytes]; a trailing CDR payload
// runs to the end of the frame.
package foxglovebridge

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	OpSubscribe   uint8 = 1
	OpUnsubscribe uint8 = 2
	OpPublish     uint8 = 3

	KindMessage  uint8 = 1
	KindSubError uint8 = 3
	KindReady    uint8 = 4

	QoSAuto            uint8 = 0
	QoSForceBestEffort uint8 = 1
)

func appendU16(dst []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(dst, v) }
func appendU32(dst []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(dst, v) }
func appendU64(dst []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(dst, v) }

func appendString(dst []byte, s string) []byte {
	dst = appendU16(dst, uint16(len(s)))
	return append(dst, s...)
}

// AppendString is the exported form used by callers building custom frames.
func AppendString(dst []byte, s string) []byte { return appendString(dst, s) }

// appendFrame wraps body in a [len][tag][body] envelope.
func appendFrame(dst []byte, tag uint8, body []byte) []byte {
	dst = appendU32(dst, uint32(1+len(body)))
	dst = append(dst, tag)
	return append(dst, body...)
}

func AppendSubscribe(dst []byte, subID uint32, topic, msgType string, qos uint8) []byte {
	var b []byte
	b = appendU32(b, subID)
	b = appendString(b, topic)
	b = appendString(b, msgType)
	b = append(b, qos)
	return appendFrame(dst, OpSubscribe, b)
}

func AppendUnsubscribe(dst []byte, subID uint32) []byte {
	return appendFrame(dst, OpUnsubscribe, appendU32(nil, subID))
}

func AppendPublish(dst []byte, topic, msgType string, cdr []byte) []byte {
	var b []byte
	b = appendString(b, topic)
	b = appendString(b, msgType)
	b = append(b, cdr...)
	return appendFrame(dst, OpPublish, b)
}

// Frame is one decoded envelope. Body aliases the reusable buffer returned by
// ReadFrame; copy anything you retain past the next ReadFrame call.
type Frame struct {
	Tag  uint8
	Body []byte
}

// maxFrame caps a single frame at 128 MiB to bound memory on a corrupt stream.
const maxFrame = 128 << 20

func ReadFrame(r io.Reader, buf []byte) (Frame, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return Frame{}, buf, fmt.Errorf("truncated frame length")
		}
		return Frame{}, buf, err // io.EOF at a clean boundary
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n == 0 {
		return Frame{}, buf, fmt.Errorf("zero-length frame")
	}
	if n > maxFrame {
		return Frame{}, buf, fmt.Errorf("frame too large: %d", n)
	}
	if uint32(cap(buf)) < n {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		return Frame{}, buf, fmt.Errorf("truncated frame body (want %d): %w", n, err)
	}
	return Frame{Tag: buf[0], Body: buf[1:]}, buf, nil
}

type Message struct {
	SubID       uint32
	TimestampNs int64
	CDR         []byte
}

func ParseMessage(body []byte) (Message, error) {
	if len(body) < 12 {
		return Message{}, fmt.Errorf("MESSAGE body too short: %d", len(body))
	}
	return Message{
		SubID:       binary.LittleEndian.Uint32(body[0:4]),
		TimestampNs: int64(binary.LittleEndian.Uint64(body[4:12])),
		CDR:         body[12:],
	}, nil
}

func readString(b []byte) (string, []byte, error) {
	if len(b) < 2 {
		return "", nil, fmt.Errorf("string length truncated")
	}
	n := int(binary.LittleEndian.Uint16(b[0:2]))
	b = b[2:]
	if len(b) < n {
		return "", nil, fmt.Errorf("string body truncated: want %d have %d", n, len(b))
	}
	return string(b[:n]), b[n:], nil
}

func ParseSubError(body []byte) (uint32, string, error) {
	if len(body) < 4 {
		return 0, "", fmt.Errorf("SUB_ERROR body too short")
	}
	subID := binary.LittleEndian.Uint32(body[0:4])
	msg, _, err := readString(body[4:])
	return subID, msg, err
}

func ParseReady(body []byte) (string, uint8, error) {
	distro, rest, err := readString(body)
	if err != nil {
		return "", 0, err
	}
	if len(rest) < 1 {
		return "", 0, fmt.Errorf("READY missing caps byte")
	}
	return distro, rest[0], nil
}
