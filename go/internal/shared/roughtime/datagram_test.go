package roughtime_test

import (
	"bytes"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
)

func TestDatagram_RoundTrip(t *testing.T) {
	original := roughtime.Datagram{
		MsgType: roughtime.MsgTypeRoughtime,
		Payload: []byte{0x01, 0x00, 0x00, 0x00, 0xAB, 0xCD},
	}
	encoded := roughtime.Encode(original)
	got, err := roughtime.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	if got.MsgType != original.MsgType {
		t.Errorf("MsgType: got %d want %d", got.MsgType, original.MsgType)
	}
	if !bytes.Equal(got.Payload, original.Payload) {
		t.Errorf("Payload mismatch")
	}
}

func TestDatagram_UnknownMsgType_Ignored(t *testing.T) {
	d := roughtime.Datagram{MsgType: 0xFF, Payload: []byte{1, 2, 3}}
	got, err := roughtime.Decode(roughtime.Encode(d))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MsgType != 0xFF {
		t.Errorf("expected MsgType 0xFF, got %d", got.MsgType)
	}
}

func TestDatagram_BadMagic(t *testing.T) {
	buf := roughtime.Encode(roughtime.Datagram{MsgType: 0x01, Payload: []byte{1}})
	buf[0] ^= 0xFF // corrupt magic
	if _, err := roughtime.Decode(buf); err == nil {
		t.Fatal("expected error for bad magic")
	}
}

func TestDatagram_TooShort(t *testing.T) {
	if _, err := roughtime.Decode([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for short input")
	}
}

func TestRoughtimePayload_RoundTrip(t *testing.T) {
	original := roughtime.RoughtimePayload{
		ServerIndex: 2,
		Nonce:       []byte{0x01, 0x02, 0x03},
		Response:    []byte{0xDE, 0xAD, 0xBE, 0xEF},
	}
	encoded := roughtime.EncodeRoughtimePayload(original)
	got, err := roughtime.DecodeRoughtimePayload(encoded)
	if err != nil {
		t.Fatalf("DecodeRoughtimePayload error: %v", err)
	}
	if got.ServerIndex != original.ServerIndex {
		t.Errorf("ServerIndex: got %d want %d", got.ServerIndex, original.ServerIndex)
	}
	if !bytes.Equal(got.Nonce, original.Nonce) {
		t.Errorf("Nonce mismatch")
	}
	if !bytes.Equal(got.Response, original.Response) {
		t.Errorf("Response mismatch")
	}
}
