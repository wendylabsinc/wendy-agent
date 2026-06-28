package diag

import "testing"

func TestRingKeepsLastN(t *testing.T) {
	ResetForTesting()
	for i := 0; i < ringCap+50; i++ {
		Record("line")
	}
	got := Recent()
	if len(got) != ringCap {
		t.Fatalf("len(Recent()) = %d, want %d", len(got), ringCap)
	}
}

func TestRingOrderOldestFirst(t *testing.T) {
	ResetForTesting()
	Record("a")
	Record("b")
	Record("c")
	got := Recent()
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("Recent() = %v, want [a b c]", got)
	}
}
