package rtps

import (
	"encoding/binary"
	"fmt"
)

// A PL_CDR parameter list is a run of (id uint16, length uint16, value[length])
// triples terminated by PID_SENTINEL. Every value is padded to a 4-byte
// boundary, and length counts the padding.

// parameterListLength measures a parameter list from the start of buf,
// including its sentinel, so a caller can skip past it.
func parameterListLength(buf []byte, order binary.ByteOrder) (int, error) {
	pos := 0
	for {
		if pos+4 > len(buf) {
			return 0, ErrShort
		}
		id := order.Uint16(buf[pos : pos+2])
		length := int(order.Uint16(buf[pos+2 : pos+4]))
		pos += 4
		if id == pidSentinel {
			return pos, nil
		}
		if pos+length > len(buf) {
			return 0, ErrShort
		}
		pos += length
	}
}

// parameter is one entry of a parameter list.
type parameter struct {
	id    uint16
	value []byte
}

// parseParameterList walks a PL_CDR payload. The payload includes its 4-byte
// encapsulation header, whose second half selects the byte order.
func parseParameterList(payload []byte) ([]parameter, binary.ByteOrder, error) {
	if len(payload) < 4 {
		return nil, nil, ErrShort
	}
	var order binary.ByteOrder
	switch id := binary.BigEndian.Uint16(payload[0:2]); id {
	case 0x0002: // PL_CDR_BE
		order = binary.BigEndian
	case 0x0003: // PL_CDR_LE
		order = binary.LittleEndian
	default:
		return nil, nil, fmt.Errorf("rtps: not a parameter list encapsulation: %#04x", id)
	}

	buf := payload[4:]
	var out []parameter
	pos := 0
	for {
		if pos+4 > len(buf) {
			return out, order, nil // tolerate a truncated tail
		}
		id := order.Uint16(buf[pos : pos+2])
		length := int(order.Uint16(buf[pos+2 : pos+4]))
		pos += 4
		if id == pidSentinel {
			return out, order, nil
		}
		if pos+length > len(buf) {
			return out, order, nil
		}
		out = append(out, parameter{id: id, value: buf[pos : pos+length]})
		pos += length
	}
}

// paramString decodes a CDR string parameter: a uint32 length including the
// NUL, then the bytes.
func paramString(v []byte, order binary.ByteOrder) (string, bool) {
	if len(v) < 4 {
		return "", false
	}
	n := int(order.Uint32(v[0:4]))
	if n == 0 || 4+n > len(v) {
		return "", false
	}
	s := v[4 : 4+n]
	if len(s) > 0 && s[len(s)-1] == 0 {
		s = s[:len(s)-1]
	}
	return string(s), true
}

// paramGUID decodes a 16-byte GUID parameter.
func paramGUID(v []byte, order binary.ByteOrder) (GUID, bool) {
	if len(v) < 16 {
		return GUID{}, false
	}
	var g GUID
	copy(g.Prefix[:], v[0:12])
	g.EntityID = binary.BigEndian.Uint32(v[12:16])
	return g, true
}

// paramLocator decodes a 24-byte locator parameter.
func paramLocator(v []byte, order binary.ByteOrder) (Locator, bool) {
	if len(v) < 24 {
		return Locator{}, false
	}
	var l Locator
	l.Kind = int32(order.Uint32(v[0:4]))
	l.Port = order.Uint32(v[4:8])
	copy(l.Addr[:], v[8:24])
	return l, true
}

// plBuilder assembles a PL_CDR_LE parameter list.
type plBuilder struct{ buf []byte }

func newPLBuilder() *plBuilder {
	// PL_CDR_LE encapsulation header, then options.
	return &plBuilder{buf: []byte{0x00, 0x03, 0x00, 0x00}}
}

// add appends one parameter, padding its value to a 4-byte boundary.
func (b *plBuilder) add(id uint16, value []byte) {
	padded := (len(value) + 3) &^ 3
	hdr := make([]byte, 4)
	binary.LittleEndian.PutUint16(hdr[0:2], id)
	binary.LittleEndian.PutUint16(hdr[2:4], uint16(padded))
	b.buf = append(b.buf, hdr...)
	b.buf = append(b.buf, value...)
	for i := len(value); i < padded; i++ {
		b.buf = append(b.buf, 0)
	}
}

func (b *plBuilder) addUint32(id uint16, v uint32) {
	tmp := make([]byte, 4)
	binary.LittleEndian.PutUint32(tmp, v)
	b.add(id, tmp)
}

// addString appends a CDR string: uint32 length including the NUL, then bytes.
func (b *plBuilder) addString(id uint16, s string) {
	tmp := make([]byte, 4+len(s)+1)
	binary.LittleEndian.PutUint32(tmp[0:4], uint32(len(s)+1))
	copy(tmp[4:], s)
	b.add(id, tmp)
}

func (b *plBuilder) addGUID(id uint16, g GUID) {
	tmp := make([]byte, 16)
	copy(tmp[0:12], g.Prefix[:])
	binary.BigEndian.PutUint32(tmp[12:16], g.EntityID)
	b.add(id, tmp)
}

func (b *plBuilder) addLocator(id uint16, l Locator) {
	tmp := make([]byte, 24)
	binary.LittleEndian.PutUint32(tmp[0:4], uint32(l.Kind))
	binary.LittleEndian.PutUint32(tmp[4:8], l.Port)
	copy(tmp[8:24], l.Addr[:])
	b.add(id, tmp)
}

// addDuration appends a Duration_t: seconds then fraction.
func (b *plBuilder) addDuration(id uint16, sec uint32, frac uint32) {
	tmp := make([]byte, 8)
	binary.LittleEndian.PutUint32(tmp[0:4], sec)
	binary.LittleEndian.PutUint32(tmp[4:8], frac)
	b.add(id, tmp)
}

// addReliability appends a ReliabilityQosPolicy: kind then max_blocking_time.
func (b *plBuilder) addReliability(kind uint32) {
	tmp := make([]byte, 12)
	binary.LittleEndian.PutUint32(tmp[0:4], kind)
	binary.LittleEndian.PutUint32(tmp[4:8], 0)
	binary.LittleEndian.PutUint32(tmp[8:12], 0x19999999) // ~100ms, conventional
	b.add(pidReliability, tmp)
}

// finish terminates the list with a sentinel and returns the payload.
func (b *plBuilder) finish() []byte {
	hdr := make([]byte, 4)
	binary.LittleEndian.PutUint16(hdr[0:2], pidSentinel)
	binary.LittleEndian.PutUint16(hdr[2:4], 0)
	return append(b.buf, hdr...)
}
