package foxglovecdr

import (
	"encoding/binary"
	"fmt"
	"math"
)

// typeSize returns the wire size in bytes of a primitive type, which is also
// its alignment requirement in CDR. A size of 1 means "no alignment
// constraint" (every offset is a multiple of 1).
func typeSize(t string) int {
	switch t {
	case "bool", "byte", "char", "int8", "uint8":
		return 1
	case "int16", "uint16":
		return 2
	case "int32", "uint32", "float32":
		return 4
	case "int64", "uint64", "float64":
		return 8
	default:
		// string/wstring are length-prefixed (see string handling) and nested
		// messages have no intrinsic size; callers never ask typeSize for them.
		return 0
	}
}

// reader walks a CDR body. All alignment is measured from the body origin,
// which is the first byte AFTER the 4-byte encapsulation header. We keep the
// full buffer and track pos as an absolute index into it, but compute
// alignment relative to bodyStart so that padding matches the OMG CDR rule
// "each primitive of size N begins at an offset that is a multiple of N,
// counted from the start of the encapsulated stream body".
type reader struct {
	buf       []byte
	pos       int
	bodyStart int
	order     binary.ByteOrder
}

// align advances pos to the next offset that is a multiple of n relative to the
// body origin, verifying the skipped padding bytes stay in bounds.
func (r *reader) align(n int) error {
	if n <= 1 {
		return nil
	}
	rel := r.pos - r.bodyStart
	pad := (n - (rel % n)) % n
	if r.pos+pad > len(r.buf) {
		return fmt.Errorf("cdr: truncated at alignment padding (need %d bytes)", pad)
	}
	r.pos += pad
	return nil
}

// take returns the next n raw bytes, advancing pos, or an error if fewer than n
// bytes remain.
func (r *reader) take(n int) ([]byte, error) {
	if r.pos+n > len(r.buf) {
		return nil, fmt.Errorf("cdr: truncated (need %d bytes, have %d)", n, len(r.buf)-r.pos)
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}

// Decode reads a full CDR message (4-byte encapsulation header + body) into a
// map[string]any per the root schema.
//
// Encapsulation header (bytes 0..3):
//
//	byte0 = 0x00
//	byte1 = representation id: 0x00 = CDR_BE, 0x01 = CDR_LE. 0x02/0x03 are
//	        PL_CDR (parameter list) and are rejected.
//	byte2..3 = options (ignored).
//
// The body begins at offset 4; alignment is measured from that origin (i.e.
// the header bytes do NOT count toward alignment).
//
// Value mapping: nested messages -> map[string]any; arrays/sequences -> []any;
// signed integers -> int64; unsigned integers (incl. byte/char) -> uint64;
// floats -> float64; bool -> bool; string -> string.
func Decode(schema map[string]*Message, root *Message, data []byte) (map[string]any, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("cdr: message shorter than 4-byte encapsulation header")
	}
	if data[0] != 0x00 {
		return nil, fmt.Errorf("cdr: unexpected encapsulation byte0 0x%02x", data[0])
	}
	var order binary.ByteOrder
	switch data[1] {
	case 0x00:
		order = binary.BigEndian
	case 0x01:
		order = binary.LittleEndian
	case 0x02, 0x03:
		return nil, fmt.Errorf("cdr: PL_CDR (parameter-list) encoding 0x%02x is not supported", data[1])
	default:
		return nil, fmt.Errorf("cdr: unknown representation id 0x%02x", data[1])
	}

	r := &reader{buf: data, pos: 4, bodyStart: 4, order: order}
	return r.readMessage(schema, root)
}

func (r *reader) readMessage(schema map[string]*Message, msg *Message) (map[string]any, error) {
	out := make(map[string]any, len(msg.Fields))
	for _, f := range msg.Fields {
		v, err := r.readField(schema, f)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.Name, err)
		}
		out[f.Name] = v
	}
	return out, nil
}

func (r *reader) readField(schema map[string]*Message, f Field) (any, error) {
	switch f.Array {
	case ArrayNone:
		return r.readElement(schema, f.Type)
	case ArrayFixed:
		out := make([]any, f.ArrayLen)
		for i := 0; i < f.ArrayLen; i++ {
			v, err := r.readElement(schema, f.Type)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case ArraySequence:
		n, err := r.readUint32()
		if err != nil {
			return nil, err
		}
		out := make([]any, n)
		for i := uint32(0); i < n; i++ {
			v, err := r.readElement(schema, f.Type)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown array kind %d", f.Array)
	}
}

// readElement reads a single element of the given base type.
func (r *reader) readElement(schema map[string]*Message, typ string) (any, error) {
	if nested, ok := schema[typ]; ok {
		return r.readMessage(schema, nested)
	}
	switch typ {
	case "string", "wstring":
		return r.readString()
	case "bool":
		b, err := r.take(1)
		if err != nil {
			return nil, err
		}
		return b[0] != 0, nil
	case "byte", "char", "uint8":
		b, err := r.take(1)
		if err != nil {
			return nil, err
		}
		return uint64(b[0]), nil
	case "int8":
		b, err := r.take(1)
		if err != nil {
			return nil, err
		}
		return int64(int8(b[0])), nil
	case "uint16":
		v, err := r.readUint(2)
		return v, err
	case "int16":
		v, err := r.readUint(2)
		if err != nil {
			return nil, err
		}
		return int64(int16(v.(uint64))), nil
	case "uint32":
		v, err := r.readUint(4)
		return v, err
	case "int32":
		v, err := r.readUint(4)
		if err != nil {
			return nil, err
		}
		return int64(int32(v.(uint64))), nil
	case "uint64":
		v, err := r.readUint(8)
		return v, err
	case "int64":
		v, err := r.readUint(8)
		if err != nil {
			return nil, err
		}
		return int64(v.(uint64)), nil
	case "float32":
		v, err := r.readUint(4)
		if err != nil {
			return nil, err
		}
		return float64(math.Float32frombits(uint32(v.(uint64)))), nil
	case "float64":
		v, err := r.readUint(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(v.(uint64)), nil
	default:
		return nil, fmt.Errorf("cdr: cannot decode type %q", typ)
	}
}

// readUint aligns to size, reads size bytes and returns them as a uint64.
func (r *reader) readUint(size int) (any, error) {
	if err := r.align(size); err != nil {
		return nil, err
	}
	b, err := r.take(size)
	if err != nil {
		return nil, err
	}
	switch size {
	case 2:
		return uint64(r.order.Uint16(b)), nil
	case 4:
		return uint64(r.order.Uint32(b)), nil
	case 8:
		return r.order.Uint64(b), nil
	default:
		return nil, fmt.Errorf("cdr: unsupported integer size %d", size)
	}
}

// readUint32 reads a 4-aligned uint32 length/count prefix.
func (r *reader) readUint32() (uint32, error) {
	if err := r.align(4); err != nil {
		return 0, err
	}
	b, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return r.order.Uint32(b), nil
}

// readString reads a CDR string: a 4-aligned uint32 length that INCLUDES the
// trailing NUL, then that many bytes (the last of which is the NUL). The
// returned Go string excludes the NUL.
func (r *reader) readString() (string, error) {
	n, err := r.readUint32()
	if err != nil {
		return "", err
	}
	if n == 0 {
		// A well-formed CDR string always includes the NUL, so length 0 is
		// technically malformed, but we tolerate it as the empty string.
		return "", nil
	}
	b, err := r.take(int(n))
	if err != nil {
		return "", err
	}
	// Drop the trailing NUL.
	return string(b[:n-1]), nil
}
