package commands

import "testing"

// The embedded catalog is maintained by pasting wendy-lite/catalog.json over
// the string literal, so this guards against a botched paste: it must parse,
// list at least one board, and every variant must be fully populated.
func TestWendyLiteVariants(t *testing.T) {
	variants, err := WendyLiteVariants()
	if err != nil {
		t.Fatalf("WendyLiteVariants() error: %v", err)
	}
	if len(variants) == 0 {
		t.Fatal("WendyLiteVariants() returned no variants")
	}

	seen := make(map[string]bool)
	for _, v := range variants {
		if v.Board == "" || v.DisplayName == "" {
			t.Errorf("variant %+v has an empty field", v)
		}
		if v.Version != "(latest)" {
			t.Errorf("variant %s: Version = %q, want %q", v.Board, v.Version, "(latest)")
		}
		if seen[v.Board] {
			t.Errorf("duplicate board %q", v.Board)
		}
		seen[v.Board] = true
	}

	if !seen["esp32c6_generic"] {
		t.Error("expected board esp32c6_generic in the catalog")
	}
}

// WendyLiteFirmwareID maps a catalog board to the key the GCS firmware manifests
// are published under: the board's binary name minus the wendy_mcu_ prefix.
func TestWendyLiteFirmwareID(t *testing.T) {
	cases := []struct {
		board string
		want  string
	}{
		{"esp32c6_generic", "esp32c6"},
		{"esp32c6_generic_native", "esp32c6_native"},
		{"esp32p4_waveshare_lcd_4b", "esp32p4_waveshare_lcd_4b"},
	}
	for _, tc := range cases {
		got, err := WendyLiteFirmwareID(tc.board)
		if err != nil {
			t.Errorf("WendyLiteFirmwareID(%q) error: %v", tc.board, err)
			continue
		}
		if got != tc.want {
			t.Errorf("WendyLiteFirmwareID(%q) = %q, want %q", tc.board, got, tc.want)
		}
	}

	if _, err := WendyLiteFirmwareID("no_such_board"); err == nil {
		t.Error("WendyLiteFirmwareID(no_such_board) = nil error, want an error")
	}
}
