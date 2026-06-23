package roughtime

import (
	"encoding/binary"
	"fmt"
)

const (
	datagramMagic   uint32 = 0x57454E44 // "WEND"
	datagramVersion uint8  = 1

	// MsgTypeRoughtime is the only defined message type; future types will be added here.
	MsgTypeRoughtime uint8 = 0x01
)

// Datagram is the extensible WendyDatagram envelope carried over UDP multicast.
type Datagram struct {
	MsgType uint8
	Payload []byte
}

// Encode serialises d to the WendyDatagram wire format.
func Encode(d Datagram) []byte {
	buf := make([]byte, 8+len(d.Payload))
	binary.BigEndian.PutUint32(buf[0:4], datagramMagic)
	buf[4] = datagramVersion
	buf[5] = d.MsgType
	binary.BigEndian.PutUint16(buf[6:8], uint16(len(d.Payload)))
	copy(buf[8:], d.Payload)
	return buf
}

// Decode parses a WendyDatagram from wire bytes.
// Returns an error for bad magic, unsupported version, or truncated payload.
// Unknown MsgType values are returned without error — callers should ignore them.
func Decode(b []byte) (Datagram, error) {
	if len(b) < 8 {
		return Datagram{}, fmt.Errorf("datagram too short: %d bytes", len(b))
	}
	if binary.BigEndian.Uint32(b[0:4]) != datagramMagic {
		return Datagram{}, fmt.Errorf("wendy datagram: bad magic 0x%X", binary.BigEndian.Uint32(b[0:4]))
	}
	if b[4] != datagramVersion {
		return Datagram{}, fmt.Errorf("wendy datagram: unsupported version %d", b[4])
	}
	payloadLen := int(binary.BigEndian.Uint16(b[6:8]))
	if len(b) < 8+payloadLen {
		return Datagram{}, fmt.Errorf("wendy datagram: payload truncated (have %d want %d)", len(b)-8, payloadLen)
	}
	payload := make([]byte, payloadLen)
	copy(payload, b[8:8+payloadLen])
	return Datagram{MsgType: b[5], Payload: payload}, nil
}

// RoughtimePayload is the msg_type=0x01 payload: a relay of a raw Roughtime server response.
type RoughtimePayload struct {
	ServerIndex uint8  // index into the agent's baked-in server list
	Nonce       []byte // request nonce used to obtain Response; optional for old payloads
	Response    []byte // raw bytes received from the Roughtime server
}

// EncodeRoughtimePayload serialises p to bytes for use as Datagram.Payload.
func EncodeRoughtimePayload(p RoughtimePayload) []byte {
	buf := make([]byte, 4+len(p.Nonce)+len(p.Response))
	buf[0] = p.ServerIndex
	buf[1] = uint8(len(p.Nonce))
	// buf[2:4] reserved, zero
	copy(buf[4:], p.Nonce)
	copy(buf[4+len(p.Nonce):], p.Response)
	return buf
}

// DecodeRoughtimePayload parses a Roughtime relay payload.
func DecodeRoughtimePayload(b []byte) (RoughtimePayload, error) {
	if len(b) < 4 {
		return RoughtimePayload{}, fmt.Errorf("roughtime payload too short: %d bytes", len(b))
	}
	nonceLen := int(b[1])
	if len(b) < 4+nonceLen {
		return RoughtimePayload{}, fmt.Errorf("roughtime payload nonce truncated (have %d want %d)", len(b)-4, nonceLen)
	}
	nonce := make([]byte, nonceLen)
	copy(nonce, b[4:4+nonceLen])
	resp := make([]byte, len(b)-4-nonceLen)
	copy(resp, b[4+nonceLen:])
	return RoughtimePayload{ServerIndex: b[0], Nonce: nonce, Response: resp}, nil
}
