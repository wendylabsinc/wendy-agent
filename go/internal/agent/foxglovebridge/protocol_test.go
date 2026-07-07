package foxglovebridge

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestSubscribeRoundTripFrames(t *testing.T) {
	// A SUBSCRIBE command followed by a MESSAGE event, back-to-back in one stream.
	var stream []byte
	stream = AppendSubscribe(stream, 7, "/img", "sensor_msgs/msg/Image", QoSAuto)

	// Hand-build a MESSAGE frame the way the bridge would, to prove ParseMessage.
	var msg []byte
	msg = appendU32(msg, 7)
	msg = appendU64(msg, 123456789)
	msg = append(msg, 0xDE, 0xAD)
	var evt []byte
	evt = appendFrame(evt, KindMessage, msg)
	stream = append(stream, evt...)

	r := bytes.NewReader(stream)
	var buf []byte

	f1, buf, err := ReadFrame(r, buf)
	if err != nil {
		t.Fatalf("read subscribe: %v", err)
	}
	if f1.Tag != OpSubscribe {
		t.Fatalf("tag = %d, want %d", f1.Tag, OpSubscribe)
	}

	f2, buf, err := ReadFrame(r, buf)
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	m, err := ParseMessage(f2.Body)
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	if m.SubID != 7 || m.TimestampNs != 123456789 || !bytes.Equal(m.CDR, []byte{0xDE, 0xAD}) {
		t.Fatalf("message = %+v", m)
	}

	if _, _, err := ReadFrame(r, buf); err != io.EOF {
		t.Fatalf("want clean EOF, got %v", err)
	}
}

func TestReadyAndSubError(t *testing.T) {
	var ready []byte
	ready = appendString(ready, "jazzy")
	ready = append(ready, 0x01)
	distro, caps, err := ParseReady(ready)
	if err != nil || distro != "jazzy" || caps != 0x01 {
		t.Fatalf("ready = %q %d %v", distro, caps, err)
	}

	var se []byte
	se = appendU32(se, 9)
	se = appendString(se, "boom")
	id, msg, err := ParseSubError(se)
	if err != nil || id != 9 || msg != "boom" {
		t.Fatalf("suberror = %d %q %v", id, msg, err)
	}
}

func TestReadFrameTruncated(t *testing.T) {
	// length says 10 but only 3 bytes follow.
	stream := append(appendU32(nil, 10), 1, 2, 3)
	_, _, err := ReadFrame(bytesReader(stream), nil)
	if err == nil {
		t.Fatal("want truncation error")
	}
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// readU16LEString is a test helper that reads a u16-prefixed LE string from the given buffer
// and returns the string, remaining buffer, and any error.
func readU16LEString(b []byte) (string, []byte, error) {
	if len(b) < 2 {
		return "", nil, io.ErrUnexpectedEOF
	}
	n := int(binary.LittleEndian.Uint16(b[0:2]))
	b = b[2:]
	if len(b) < n {
		return "", nil, io.ErrUnexpectedEOF
	}
	return string(b[:n]), b[n:], nil
}

func TestAppendSubscribeLayout(t *testing.T) {
	// Build a SUBSCRIBE frame.
	var data []byte
	data = AppendSubscribe(data, 7, "/img", "sensor_msgs/msg/Image", QoSAuto)

	// Decode with ReadFrame.
	f, _, err := ReadFrame(bytesReader(data), nil)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f.Tag != OpSubscribe {
		t.Fatalf("tag = %d, want %d", f.Tag, OpSubscribe)
	}

	// Verify body layout: [u32 subID][u16 len topic][topic][u16 len msgType][msgType][u8 qos].
	b := f.Body
	if len(b) < 4 {
		t.Fatalf("body too short for subID: len=%d", len(b))
	}
	subID := binary.LittleEndian.Uint32(b[0:4])
	if subID != 7 {
		t.Fatalf("subID = %d, want 7", subID)
	}
	b = b[4:]

	topic, b, err := readU16LEString(b)
	if err != nil {
		t.Fatalf("read topic: %v", err)
	}
	if topic != "/img" {
		t.Fatalf("topic = %q, want %q", topic, "/img")
	}

	msgType, b, err := readU16LEString(b)
	if err != nil {
		t.Fatalf("read msgType: %v", err)
	}
	if msgType != "sensor_msgs/msg/Image" {
		t.Fatalf("msgType = %q, want %q", msgType, "sensor_msgs/msg/Image")
	}

	if len(b) != 1 {
		t.Fatalf("remaining bytes = %d, want 1", len(b))
	}
	if b[0] != QoSAuto {
		t.Fatalf("qos = %d, want %d", b[0], QoSAuto)
	}
}

func TestAppendPublishLayout(t *testing.T) {
	// Build a PUBLISH frame.
	var data []byte
	data = AppendPublish(data, "/cmd", "geometry_msgs/msg/Twist", []byte{0x01, 0x02, 0x03})

	// Decode with ReadFrame.
	f, _, err := ReadFrame(bytesReader(data), nil)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f.Tag != OpPublish {
		t.Fatalf("tag = %d, want %d", f.Tag, OpPublish)
	}

	// Verify body layout: [u16 len topic][topic][u16 len msgType][msgType][cdr...].
	b := f.Body

	topic, b, err := readU16LEString(b)
	if err != nil {
		t.Fatalf("read topic: %v", err)
	}
	if topic != "/cmd" {
		t.Fatalf("topic = %q, want %q", topic, "/cmd")
	}

	msgType, b, err := readU16LEString(b)
	if err != nil {
		t.Fatalf("read msgType: %v", err)
	}
	if msgType != "geometry_msgs/msg/Twist" {
		t.Fatalf("msgType = %q, want %q", msgType, "geometry_msgs/msg/Twist")
	}

	// Remaining should be the CDR payload.
	if !bytes.Equal(b, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("cdr = %v, want [0x01 0x02 0x03]", b)
	}
}

func TestAppendUnsubscribeLayout(t *testing.T) {
	// Build an UNSUBSCRIBE frame.
	var data []byte
	data = AppendUnsubscribe(data, 42)

	// Decode with ReadFrame.
	f, _, err := ReadFrame(bytesReader(data), nil)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f.Tag != OpUnsubscribe {
		t.Fatalf("tag = %d, want %d", f.Tag, OpUnsubscribe)
	}

	// Body should be exactly 4 bytes: the little-endian u32 42.
	if len(f.Body) != 4 {
		t.Fatalf("body len = %d, want 4", len(f.Body))
	}
	subID := binary.LittleEndian.Uint32(f.Body[0:4])
	if subID != 42 {
		t.Fatalf("subID = %d, want 42", subID)
	}
}

func TestReadFrameRejectsZeroAndOversize(t *testing.T) {
	// Zero-length frame.
	zeroFrame := []byte{0x00, 0x00, 0x00, 0x00}
	_, _, err := ReadFrame(bytesReader(zeroFrame), nil)
	if err == nil || err.Error() != "zero-length frame" {
		t.Fatalf("zero-length frame: got %v, want 'zero-length frame'", err)
	}

	// Oversize frame (exceeds 128 MiB).
	oversizeFrame := []byte{0xFF, 0xFF, 0xFF, 0xFF}
	_, _, err = ReadFrame(bytesReader(oversizeFrame), nil)
	if err == nil {
		t.Fatalf("oversize frame: got no error, want 'frame too large'")
	}
	if err.Error() != "frame too large: 4294967295" {
		t.Fatalf("oversize frame: got %v, want 'frame too large: 4294967295'", err)
	}
}
