package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOkResult_HasStructuredAndJSONText(t *testing.T) {
	r := okResult(map[string]any{"version": "1.2.3"})
	if r.IsError {
		t.Fatal("expected success result")
	}
	if r.StructuredContent == nil {
		t.Fatal("expected structured content")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, r)), &m); err != nil {
		t.Fatalf("text fallback is not valid JSON: %v", err)
	}
	if m["version"] != "1.2.3" {
		t.Errorf("version = %v", m["version"])
	}
}

// MCP requires structuredContent to be a JSON object. A bare array made every
// list-shaped tool fail client-side schema validation (WDY-2001), so okResult
// wraps any non-object payload rather than emitting one.
func TestOkResult_WrapsNonObjectPayload(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
	}{
		{"slice", []map[string]any{{"a": 1}}},
		{"empty slice", []map[string]any{}},
		{"nil slice", []map[string]any(nil)},
		{"scalar", "hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := okResult(tc.v)
			if _, ok := r.StructuredContent.(map[string]any); !ok {
				t.Fatalf("structuredContent must be an object, got %T", r.StructuredContent)
			}
			text := toolResultText(t, r)
			if text[0] != '{' {
				t.Errorf("text fallback must be a JSON object, got %s", text)
			}
		})
	}
}

func TestOkList_WrapsUnderKeyAndNormalizesNil(t *testing.T) {
	r := okList("devices", []map[string]any(nil))
	sc, ok := r.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected object structured content, got %T", r.StructuredContent)
	}
	// The key must be present with an empty list, not absent and not null —
	// agents branch on it without a nil check.
	var env map[string][]map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, r)), &env); err != nil {
		t.Fatalf("text fallback is not valid JSON: %v", err)
	}
	rows, ok := env["devices"]
	if !ok {
		t.Fatal("envelope is missing the devices key")
	}
	if len(rows) != 0 {
		t.Errorf("expected empty list, got %v", rows)
	}
	if _, ok := sc["devices"]; !ok {
		t.Error("structuredContent is missing the devices key")
	}
}

func TestOkListBounded_TruncationEnvelopeSurvives(t *testing.T) {
	big := make([]string, 0, 1000)
	for i := 0; i < 1000; i++ {
		big = append(big, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	}
	r := okListBounded("batches", big, 200)
	sc, ok := r.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected truncation envelope map, got %T", r.StructuredContent)
	}
	if sc["truncated"] != true {
		t.Errorf("expected truncated=true, got %v", sc["truncated"])
	}
}

func TestOkResultBounded_TruncatesOversize(t *testing.T) {
	big := make([]string, 0, 1000)
	for i := 0; i < 1000; i++ {
		big = append(big, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	}
	r := okResultBounded(big, 200)
	if r.IsError {
		t.Fatal("truncation is not an error result")
	}
	sc, ok := r.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected truncation envelope map, got %T", r.StructuredContent)
	}
	if sc["truncated"] != true {
		t.Errorf("expected truncated=true, got %v", sc["truncated"])
	}
}

func TestOkResultBounded_PassesSmall(t *testing.T) {
	r := okResultBounded(map[string]any{"k": "v"}, 100000)
	sc, _ := r.StructuredContent.(map[string]any)
	if sc["truncated"] == true {
		t.Error("small payload should not be truncated")
	}
}

func TestOkTextBounded_TruncatesOnRuneBoundary(t *testing.T) {
	// A string of 3-byte runes (each "世" is 3 bytes). Cutting at a byte cap
	// that lands mid-rune must back off so the result stays valid UTF-8.
	s := strings.Repeat("世", 20)  // 60 bytes
	r := okTextBounded(s, "", 10) // 10 is not a multiple of 3 → mid-rune cut
	text := toolResultText(t, r)
	if !utf8.ValidString(text) {
		t.Fatalf("truncated output is not valid UTF-8: %q", text)
	}
	if !strings.Contains(text, "truncated") {
		t.Error("expected a truncation note in the output")
	}
}

func TestOkTextBounded_PassesSmall(t *testing.T) {
	r := okTextBounded("hello", "", 100000)
	if toolResultText(t, r) != "hello" {
		t.Error("under-cap text should be returned verbatim")
	}
}
