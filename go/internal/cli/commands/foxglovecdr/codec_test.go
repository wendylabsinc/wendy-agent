package foxglovecdr

import (
	"math"
	"reflect"
	"testing"
)

// pointSchema: geometry_msgs/Point = {float64 x, y, z}
func pointSetup(t *testing.T) (map[string]*Message, *Message) {
	t.Helper()
	schema, root, err := ParseSchema("float64 x\nfloat64 y\nfloat64 z\n")
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	return schema, root
}

func le64(f float64) []byte {
	b := make([]byte, 8)
	bits := math.Float64bits(f)
	for i := 0; i < 8; i++ {
		b[i] = byte(bits >> (8 * i))
	}
	return b
}

func TestDecodePoint(t *testing.T) {
	schema, root := pointSetup(t)
	var data []byte
	data = append(data, 0x00, 0x01, 0x00, 0x00) // CDR_LE header
	data = append(data, le64(1.0)...)
	data = append(data, le64(2.0)...)
	data = append(data, le64(3.0)...)

	got, err := Decode(schema, root, data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := map[string]any{"x": 1.0, "y": 2.0, "z": 3.0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Decode:\n got %+v\nwant %+v", got, want)
	}
}

func TestEncodePoint(t *testing.T) {
	schema, root := pointSetup(t)
	var want []byte
	want = append(want, 0x00, 0x01, 0x00, 0x00)
	want = append(want, le64(1.0)...)
	want = append(want, le64(2.0)...)
	want = append(want, le64(3.0)...)

	got, err := Encode(schema, root, map[string]any{"x": 1.0, "y": 2.0, "z": 3.0})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Encode:\n got % x\nwant % x", got, want)
	}
}

// strStruct: {string name; uint32 count; uint8[] data}
// Exercises string length+NUL, alignment padding after the string before the
// uint32, and a sequence.
func strStructSetup(t *testing.T) (map[string]*Message, *Message) {
	t.Helper()
	schema, root, err := ParseSchema("string name\nuint32 count\nuint8[] data\n")
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	return schema, root
}

// Build the expected CDR_LE byte vector for {name:"ab", count:7, data:[1,2,3]}.
//
//	header:            00 01 00 00              (offset 0..3, body origin at 4)
//	string len (u32):  03 00 00 00              (body off 0, "ab\0" = 3 bytes)
//	string bytes:      61 62 00                 (body off 4..6)
//	pad to align 4:    00                       (body off 7 -> next u32 at 8)
//	count (u32):       07 00 00 00              (body off 8)
//	seq len (u32):     03 00 00 00              (body off 12)
//	data (uint8 x3):   01 02 03                 (body off 16..18)
func strStructBytes() []byte {
	var d []byte
	d = append(d, 0x00, 0x01, 0x00, 0x00)
	d = append(d, 0x03, 0x00, 0x00, 0x00) // string len 3
	d = append(d, 'a', 'b', 0x00)          // "ab\0"
	d = append(d, 0x00)                     // pad
	d = append(d, 0x07, 0x00, 0x00, 0x00) // count 7
	d = append(d, 0x03, 0x00, 0x00, 0x00) // seq len 3
	d = append(d, 0x01, 0x02, 0x03)        // data
	return d
}

func TestDecodeStrStruct(t *testing.T) {
	schema, root := strStructSetup(t)
	got, err := Decode(schema, root, strStructBytes())
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := map[string]any{
		"name":  "ab",
		"count": uint64(7),
		"data":  []any{uint64(1), uint64(2), uint64(3)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Decode:\n got %+v\nwant %+v", got, want)
	}
}

func TestEncodeStrStruct(t *testing.T) {
	schema, root := strStructSetup(t)
	v := map[string]any{
		"name":  "ab",
		"count": uint64(7),
		"data":  []any{uint64(1), uint64(2), uint64(3)},
	}
	got, err := Encode(schema, root, v)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !reflect.DeepEqual(got, strStructBytes()) {
		t.Errorf("Encode:\n got % x\nwant % x", got, strStructBytes())
	}
}

func TestRoundTripPoint(t *testing.T) {
	schema, root := pointSetup(t)
	v := map[string]any{"x": 1.5, "y": -2.25, "z": 3.75}
	enc, err := Encode(schema, root, v)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := Decode(schema, root, enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(dec, v) {
		t.Errorf("round-trip:\n got %+v\nwant %+v", dec, v)
	}
}

func TestRoundTripStrStruct(t *testing.T) {
	schema, root := strStructSetup(t)
	v := map[string]any{
		"name":  "hello",
		"count": uint64(42),
		"data":  []any{uint64(9), uint64(8)},
	}
	enc, err := Encode(schema, root, v)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := Decode(schema, root, enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(dec, v) {
		t.Errorf("round-trip:\n got %+v\nwant %+v", dec, v)
	}
}

func TestDecodeRejectsParameterList(t *testing.T) {
	schema, root := pointSetup(t)
	data := []byte{0x00, 0x02, 0x00, 0x00} // PL_CDR_BE
	if _, err := Decode(schema, root, data); err == nil {
		t.Errorf("expected error for PL_CDR representation id")
	}
}

func TestDecodeTruncation(t *testing.T) {
	schema, root := pointSetup(t)
	data := []byte{0x00, 0x01, 0x00, 0x00, 0x01, 0x02} // header + 2 stray bytes
	if _, err := Decode(schema, root, data); err == nil {
		t.Errorf("expected truncation error")
	}
}
