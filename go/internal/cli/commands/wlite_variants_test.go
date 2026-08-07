package commands

import "testing"

// The embedded catalog is maintained by pasting wendy-lite/catalog.json over
// the string literal, so this guards against a botched paste: it must parse,
// list at least one board, and every board must be fully populated.
func TestWendyLiteBoards(t *testing.T) {
	boards, err := WendyLiteBoards()
	if err != nil {
		t.Fatalf("WendyLiteBoards() error: %v", err)
	}
	if len(boards) == 0 {
		t.Fatal("WendyLiteBoards() returned no boards")
	}

	seen := make(map[string]bool)
	for _, b := range boards {
		if b.Board == "" || b.Target == "" || b.DisplayName == "" {
			t.Errorf("board %+v has an empty field", b)
		}
		if b.Version != "(latest)" {
			t.Errorf("board %s: Version = %q, want %q", b.Board, b.Version, "(latest)")
		}
		if seen[b.Board] {
			t.Errorf("duplicate board %q", b.Board)
		}
		seen[b.Board] = true
	}

	if !seen["esp32c6_generic"] {
		t.Error("expected board esp32c6_generic in the catalog")
	}
}

// TestWendyLiteTargets guards the same embedded-catalog paste as
// TestWendyLiteBoards, but for the catalog's targets array.
func TestWendyLiteTargets(t *testing.T) {
	targets, err := WendyLiteTargets()
	if err != nil {
		t.Fatalf("WendyLiteTargets() error: %v", err)
	}
	if len(targets) == 0 {
		t.Fatal("WendyLiteTargets() returned no targets")
	}

	seen := make(map[string]bool)
	for _, tg := range targets {
		if tg.Name == "" || tg.DisplayName == "" {
			t.Errorf("target %+v has an empty field", tg)
		}
		seen[tg.Name] = true
	}

	if !seen["esp32c6"] {
		t.Error("expected target esp32c6 in the catalog")
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
