package tui

import (
	"strings"
	"testing"
)

func TestNewHintRotatorPicksHintFromList(t *testing.T) {
	r := newHintRotator()
	if r.current == "" {
		t.Fatal("expected newHintRotator to pick a non-empty starting hint")
	}
	found := false
	for _, h := range ProgressHints {
		if h == r.current {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("starting hint %q is not in ProgressHints", r.current)
	}
}

func TestHintRotatorNextAvoidsImmediateRepeat(t *testing.T) {
	r := hintRotator{hints: []string{"a", "b", "c"}, current: "a"}
	prev := r.current
	for i := 0; i < 50; i++ {
		r.next()
		if r.current == prev {
			t.Fatalf("next() repeated hint %q on iteration %d", r.current, i)
		}
		prev = r.current
	}
}

func TestHintRotatorNextNoopForSingleHint(t *testing.T) {
	r := hintRotator{hints: []string{"only"}, current: "only"}
	r.next()
	if r.current != "only" {
		t.Fatalf("expected single-hint rotator to stay %q, got %q", "only", r.current)
	}
}

func TestHintRotatorNextNoopForEmpty(t *testing.T) {
	r := hintRotator{} // must not panic
	r.next()
	if r.current != "" {
		t.Fatalf("expected empty rotator to stay empty, got %q", r.current)
	}
}

func TestHintRotatorViewRendersHint(t *testing.T) {
	r := hintRotator{hints: []string{"do a thing"}, current: "do a thing"}
	v := r.view()
	if !strings.Contains(v, "do a thing") {
		t.Fatalf("view %q does not contain the hint text", v)
	}
	if !strings.Contains(v, "💡") {
		t.Fatalf("view %q does not contain the hint marker", v)
	}
}

func TestHintRotatorViewEmptyForNoHint(t *testing.T) {
	r := hintRotator{}
	if v := r.view(); v != "" {
		t.Fatalf("expected empty view for empty rotator, got %q", v)
	}
}
