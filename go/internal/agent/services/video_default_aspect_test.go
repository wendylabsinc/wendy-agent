package services

import "testing"

// TestHasStandardAspect pins the rule that keeps "device default" off a
// metadata-carrying mode. The thermal entries are the real advertised list from
// a TOPDON TC001 on wendy-box-theta / ccr1.
func TestHasStandardAspect(t *testing.T) {
	standard := [][2]uint32{
		{320, 240}, {640, 480}, {1280, 720}, {1920, 1080}, {800, 600},
		{256, 192},  // thermal sensor native, 4:3
		{512, 384},  // thermal image-only, 4:3 — the mode we want chosen
		{1280, 800}, // 16:10
		{720, 480},  // 3:2
	}
	for _, s := range standard {
		if !hasStandardAspect(s[0], s[1]) {
			t.Errorf("%dx%d should be a standard aspect", s[0], s[1])
		}
	}

	nonStandard := [][2]uint32{
		{512, 484}, // image + ~100 metadata rows — the one that caused the green band
		{256, 392}, // image + data stacked (portrait)
		{256, 196}, // sensor + 4 metadata rows
		{520, 192},
		{644, 384},
		{4, 12305}, // malformed descriptor
		{60, 3299}, // malformed descriptor
		{0, 0},
	}
	for _, s := range nonStandard {
		if hasStandardAspect(s[0], s[1]) {
			t.Errorf("%dx%d should NOT be a standard aspect", s[0], s[1])
		}
	}
}

// TestStandardAspectBeatsLargerArea is the regression this change exists for:
// 512x484 has the larger area, but 512x384 is the actual picture.
func TestStandardAspectBeatsLargerArea(t *testing.T) {
	withMeta := uint64(512) * uint64(484)
	imageOnly := uint64(512) * uint64(384)
	if withMeta <= imageOnly {
		t.Fatal("premise wrong: the metadata mode should have the larger area")
	}
	if !hasStandardAspect(512, 384) {
		t.Fatal("512x384 must be selectable as standard")
	}
	if hasStandardAspect(512, 484) {
		t.Fatal("512x484 must not be selectable as standard")
	}
}
