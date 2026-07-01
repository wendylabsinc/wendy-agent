package foxglovecdr

import (
	"reflect"
	"testing"
)

func TestYAMLRoundTrip(t *testing.T) {
	// Use non-whole floats and explicit ints. Whole-number floats (e.g. 1.0)
	// cannot survive a YAML round-trip as float64 because YAML renders them as
	// `1` and re-parses them as int; that ambiguity is a property of the text
	// format, not this bridge, so we avoid it in the fixture.
	v := map[string]any{
		"linear":  map[string]any{"x": 1.25, "y": -0.5, "z": 3.5},
		"angular": map[string]any{"z": 0.5},
		"name":    "robot",
		"enabled": true,
		"count":   int(3),
		"path":    []any{1.5, 2.5, 3.5},
	}
	s, err := ToYAML(v)
	if err != nil {
		t.Fatalf("ToYAML: %v", err)
	}
	got, err := FromYAML(s)
	if err != nil {
		t.Fatalf("FromYAML(%q): %v", s, err)
	}
	if !reflect.DeepEqual(got, v) {
		t.Errorf("round-trip:\n got %+v\nwant %+v\n(yaml=%q)", got, v, s)
	}
}
