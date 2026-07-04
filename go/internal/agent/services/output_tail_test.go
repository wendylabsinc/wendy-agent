package services

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestLineRingKeepsMostRecentLines(t *testing.T) {
	r := newLineRing(3)
	for i := 1; i <= 5; i++ {
		r.push(fmt.Sprintf("line %d", i))
	}

	got := r.tail()
	want := []string{"line 3", "line 4", "line 5"}
	if len(got) != len(want) {
		t.Fatalf("tail length = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tail[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLineRingFewerThanCapacity(t *testing.T) {
	r := newLineRing(10)
	r.push("only")
	got := r.tail()
	if len(got) != 1 || got[0] != "only" {
		t.Fatalf("tail = %v, want [only]", got)
	}
}

func TestLineRingSkipsBlankLines(t *testing.T) {
	r := newLineRing(5)
	r.push("real")
	r.push("")
	r.push("   ")
	r.push("also real")
	got := r.tail()
	want := []string{"real", "also real"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tail = %v, want %v", got, want)
	}
}

// Run under -race to catch unsynchronized access from the concurrent
// stdout/stderr scan goroutines.
func TestLineRingConcurrentPush(t *testing.T) {
	r := newLineRing(50)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				r.push("x")
			}
		}()
	}
	wg.Wait()
	if got := len(r.tail()); got != 50 {
		t.Fatalf("tail length = %d, want 50", got)
	}
}
