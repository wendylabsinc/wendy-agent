package diskspace

import "testing"

func TestThresholdsOrdered(t *testing.T) {
	if !(WarnFreePct > FailFreePct) {
		t.Fatalf("WarnFreePct (%v) must be greater than FailFreePct (%v)", WarnFreePct, FailFreePct)
	}
	if FailFreePct <= 0 || WarnFreePct >= 100 {
		t.Fatalf("thresholds out of range: warn=%v fail=%v", WarnFreePct, FailFreePct)
	}
}
