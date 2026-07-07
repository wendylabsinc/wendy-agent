package foxglovebridge

import (
	"bytes"
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
