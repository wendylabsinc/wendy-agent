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
