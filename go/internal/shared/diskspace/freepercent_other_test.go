//go:build !linux

package diskspace

import "testing"

func TestFreePercent_UnsupportedIsNoOp(t *testing.T) {
	pct, ok := FreePercent("/")
	if ok {
		t.Fatal("FreePercent should report ok=false off-device")
	}
	if pct != 0 {
		t.Fatalf("FreePercent = %v, want 0 off-device", pct)
	}
}
