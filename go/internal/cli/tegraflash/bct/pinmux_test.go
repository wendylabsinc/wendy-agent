package bct

import "testing"

// TestPinMuxRegBaseIndices verifies every pinMuxEntries entry has an in-range
// f1c index into pinMuxRegBase. (The reset-default completeness for pins that a
// board actually uses is enforced at runtime by pinPair, which errors on a
// missing default, and exercised end-to-end by TestMB1BCTMatchesGolden. A pin in
// the full chip table that no board DTB references legitimately may lack a
// default, so a blanket table-completeness assertion would be too strict.)
func TestPinMuxRegBaseIndices(t *testing.T) {
	for pin, e := range pinMuxEntries {
		if int(e.f1c) >= len(pinMuxRegBase) {
			t.Errorf("pin %q: f1c %d out of range for pinMuxRegBase (len %d)", pin, e.f1c, len(pinMuxRegBase))
		}
	}
}
