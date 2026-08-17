package adb

import "testing"

func TestSelectADBPathExactModeNeverFallsBack(t *testing.T) {
	if index, fallback, err := selectADBPath([]string{"1-2"}, "1-1", false); err != nil || index != 0 || !fallback {
		t.Fatalf("legacy unique fallback = index %d fallback %v err %v", index, fallback, err)
	}
	if _, _, err := selectADBPath([]string{"1-2"}, "1-1", true); err == nil {
		t.Fatal("exact mode accepted an off-port gadget")
	}
	if index, fallback, err := selectADBPath([]string{"1-2", "1-1"}, "1-1", true); err != nil || index != 1 || fallback {
		t.Fatalf("exact match = index %d fallback %v err %v", index, fallback, err)
	}
	if _, _, err := selectADBPath([]string{"1-2", "3-2"}, "1-1", false); err == nil {
		t.Fatal("ambiguous legacy selection accepted")
	}
	if _, _, err := selectADBPath([]string{"1-1", "1-1"}, "1-1", true); err == nil {
		t.Fatal("duplicate exact path accepted")
	}
	if _, _, err := selectADBPath([]string{"1-1"}, "", true); err == nil {
		t.Fatal("exact mode accepted an empty controller path")
	}
	if _, _, err := selectADBPath(make([]string, maxADBDevices+1), "1-1", true); err == nil {
		t.Fatal("oversized ADB discovery accepted")
	}
}
