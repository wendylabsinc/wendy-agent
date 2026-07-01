package foxglovecdr

import (
	"reflect"
	"testing"
)

// A nested schema exercising: nested type reference, fixed array, unbounded
// sequence, a constant line (must be excluded), and a field with a default
// value (must be kept).
const nestedSchema = `# a comment
geometry_msgs/Point position
float64[3] covariance
int32[] samples
uint8 STATUS_OK=0
int32 retries 5

================================================================================
MSG: geometry_msgs/Point
float64 x
float64 y
float64 z
`

func TestParseSchemaRoot(t *testing.T) {
	schema, root, err := ParseSchema(nestedSchema)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}

	want := []Field{
		{Name: "position", Type: "geometry_msgs/Point", Array: ArrayNone, ArrayLen: 0},
		{Name: "covariance", Type: "float64", Array: ArrayFixed, ArrayLen: 3},
		{Name: "samples", Type: "int32", Array: ArraySequence, ArrayLen: 0},
		{Name: "retries", Type: "int32", Array: ArrayNone, ArrayLen: 0},
	}
	if !reflect.DeepEqual(root.Fields, want) {
		t.Errorf("root fields:\n got %+v\nwant %+v", root.Fields, want)
	}

	pt, ok := schema["geometry_msgs/Point"]
	if !ok {
		t.Fatalf("dependency geometry_msgs/Point missing; keys=%v", keys(schema))
	}
	wantPt := []Field{
		{Name: "x", Type: "float64"},
		{Name: "y", Type: "float64"},
		{Name: "z", Type: "float64"},
	}
	if !reflect.DeepEqual(pt.Fields, wantPt) {
		t.Errorf("Point fields:\n got %+v\nwant %+v", pt.Fields, wantPt)
	}
}

func TestParseSchemaBoundedForms(t *testing.T) {
	// Bounded sequence and bounded string decode to the same wire form as their
	// unbounded counterparts.
	s := `int32[<=10] data
string<=20 name
string full
`
	_, root, err := ParseSchema(s)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	want := []Field{
		{Name: "data", Type: "int32", Array: ArraySequence},
		{Name: "name", Type: "string", Array: ArrayNone},
		{Name: "full", Type: "string", Array: ArrayNone},
	}
	if !reflect.DeepEqual(root.Fields, want) {
		t.Errorf("fields:\n got %+v\nwant %+v", root.Fields, want)
	}
}

func keys(m map[string]*Message) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
