package foxglovecdr

import (
	"encoding/binary"
	"fmt"
	"math"
)

// writer builds a CDR body. Alignment padding is emitted relative to bodyStart
// (the byte after the 4-byte encapsulation header), mirroring the reader.
type writer struct {
	buf       []byte
	bodyStart int
	order     binary.ByteOrder
}

// align pads with zero bytes until the current length is a multiple of n
// relative to the body origin.
func (w *writer) align(n int) {
	if n <= 1 {
		return
	}
	rel := len(w.buf) - w.bodyStart
	pad := (n - (rel % n)) % n
	for i := 0; i < pad; i++ {
		w.buf = append(w.buf, 0)
	}
}

// Encode serializes value into a full CDR message: a CDR_LE encapsulation
// header (00 01 00 00) followed by the aligned body, laid out per the root
// schema. Numeric values may arrive as int, int64, uint64 or float64 (as from
// YAML/JSON decoding) and are coerced to the field's declared type.
//
// It returns an error on a missing field or a value that cannot be coerced.
func Encode(schema map[string]*Message, root *Message, value map[string]any) ([]byte, error) {
	w := &writer{
		buf:       []byte{0x00, 0x01, 0x00, 0x00}, // CDR_LE header
		bodyStart: 4,
		order:     binary.LittleEndian,
	}
	if err := w.writeMessage(schema, root, value); err != nil {
		return nil, err
	}
	return w.buf, nil
}

func (w *writer) writeMessage(schema map[string]*Message, msg *Message, value map[string]any) error {
	for _, f := range msg.Fields {
		v, ok := value[f.Name]
		if !ok {
			return fmt.Errorf("missing field %q", f.Name)
		}
		if err := w.writeField(schema, f, v); err != nil {
			return fmt.Errorf("field %q: %w", f.Name, err)
		}
	}
	return nil
}

func (w *writer) writeField(schema map[string]*Message, f Field, v any) error {
	switch f.Array {
	case ArrayNone:
		return w.writeElement(schema, f.Type, v)
	case ArrayFixed:
		items, err := asSlice(v)
		if err != nil {
			return err
		}
		if len(items) != f.ArrayLen {
			return fmt.Errorf("fixed array expects %d elements, got %d", f.ArrayLen, len(items))
		}
		for _, it := range items {
			if err := w.writeElement(schema, f.Type, it); err != nil {
				return err
			}
		}
		return nil
	case ArraySequence:
		items, err := asSlice(v)
		if err != nil {
			return err
		}
		w.writeUint32(uint32(len(items)))
		for _, it := range items {
			if err := w.writeElement(schema, f.Type, it); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown array kind %d", f.Array)
	}
}

func (w *writer) writeElement(schema map[string]*Message, typ string, v any) error {
	if nested, ok := schema[typ]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("nested message %q expects map, got %T", typ, v)
		}
		return w.writeMessage(schema, nested, m)
	}
	switch typ {
	case "string", "wstring":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", v)
		}
		w.writeString(s)
		return nil
	case "bool":
		b, err := asBool(v)
		if err != nil {
			return err
		}
		var by byte
		if b {
			by = 1
		}
		w.buf = append(w.buf, by)
		return nil
	case "byte", "char", "uint8":
		u, err := asUint(v)
		if err != nil {
			return err
		}
		w.buf = append(w.buf, byte(u))
		return nil
	case "int8":
		i, err := asInt(v)
		if err != nil {
			return err
		}
		w.buf = append(w.buf, byte(int8(i)))
		return nil
	case "uint16":
		u, err := asUint(v)
		if err != nil {
			return err
		}
		w.writeUint(2, u)
		return nil
	case "int16":
		i, err := asInt(v)
		if err != nil {
			return err
		}
		w.writeUint(2, uint64(uint16(int16(i))))
		return nil
	case "uint32":
		u, err := asUint(v)
		if err != nil {
			return err
		}
		w.writeUint(4, u)
		return nil
	case "int32":
		i, err := asInt(v)
		if err != nil {
			return err
		}
		w.writeUint(4, uint64(uint32(int32(i))))
		return nil
	case "uint64":
		u, err := asUint(v)
		if err != nil {
			return err
		}
		w.writeUint(8, u)
		return nil
	case "int64":
		i, err := asInt(v)
		if err != nil {
			return err
		}
		w.writeUint(8, uint64(i))
		return nil
	case "float32":
		f, err := asFloat(v)
		if err != nil {
			return err
		}
		w.writeUint(4, uint64(math.Float32bits(float32(f))))
		return nil
	case "float64":
		f, err := asFloat(v)
		if err != nil {
			return err
		}
		w.writeUint(8, math.Float64bits(f))
		return nil
	default:
		return fmt.Errorf("cdr: cannot encode type %q", typ)
	}
}

// writeUint aligns to size and writes an integer in the configured byte order.
func (w *writer) writeUint(size int, v uint64) {
	w.align(size)
	b := make([]byte, size)
	switch size {
	case 2:
		w.order.PutUint16(b, uint16(v))
	case 4:
		w.order.PutUint32(b, uint32(v))
	case 8:
		w.order.PutUint64(b, v)
	}
	w.buf = append(w.buf, b...)
}

// writeUint32 writes a 4-aligned uint32 length/count prefix.
func (w *writer) writeUint32(v uint32) {
	w.writeUint(4, uint64(v))
}

// writeString writes a CDR string: a 4-aligned uint32 length = len(s)+1, the
// string bytes, then a trailing NUL.
func (w *writer) writeString(s string) {
	w.writeUint32(uint32(len(s) + 1))
	w.buf = append(w.buf, s...)
	w.buf = append(w.buf, 0)
}

// --- numeric coercion helpers ---
//
// Values arriving from YAML/JSON decoding may be int, int64, uint64, float64,
// or occasionally a whole-number float64. These helpers coerce to the field's
// declared category, rejecting types that cannot represent the value.

func asSlice(v any) ([]any, error) {
	s, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected sequence, got %T", v)
	}
	return s, nil
}

func asBool(v any) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("expected bool, got %T", v)
	}
	return b, nil
}

func asUint(v any) (uint64, error) {
	switch n := v.(type) {
	case uint64:
		return n, nil
	case int64:
		if n < 0 {
			return 0, fmt.Errorf("negative value %d for unsigned field", n)
		}
		return uint64(n), nil
	case int:
		if n < 0 {
			return 0, fmt.Errorf("negative value %d for unsigned field", n)
		}
		return uint64(n), nil
	case float64:
		if n < 0 || n != math.Trunc(n) {
			return 0, fmt.Errorf("non-integer/negative value %v for unsigned field", n)
		}
		return uint64(n), nil
	default:
		return 0, fmt.Errorf("expected unsigned integer, got %T", v)
	}
}

func asInt(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	case float64:
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("non-integer value %v for integer field", n)
		}
		return int64(n), nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", v)
	}
}

func asFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int64:
		return float64(n), nil
	case int:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("expected float, got %T", v)
	}
}
