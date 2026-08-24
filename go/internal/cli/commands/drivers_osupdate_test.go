package commands

import "testing"

func TestCountAddons(t *testing.T) {
	for n, want := range map[int]string{1: "1 driver add-on", 2: "2 driver add-ons"} {
		if got := countAddons(n); got != want {
			t.Errorf("countAddons(%d) = %q, want %q", n, got, want)
		}
	}
}
