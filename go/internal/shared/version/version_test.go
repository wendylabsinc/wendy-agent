package version

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		// Equal.
		{"1.0.0", "1.0.0", 0},
		{"dev", "dev", 0},

		// Dev is treated as the newest version (WDY-1770), so a real
		// release is never considered ahead of a dev build.
		{"dev", "0.1.0", 1},
		{"0.1.0", "dev", -1},

		// CI branch builds carry a "-dev" suffix and are dev builds too.
		{"2026.06.30-1-dev", "2026.06.30-1", 1},
		{"2026.06.30-1", "2026.06.30-1-dev", -1},
		{"2026.06.30-1-dev", "99.99.99", 1},
		// Two dev builds (any flavor) compare equal.
		{"dev", "2026.06.30-1-dev", 0},
		{"2026.06.30-1-dev", "2026.06.30-2-dev", 0},

		// Basic semver.
		{"0.9.3", "0.9.8", -1},
		{"0.9.8", "0.9.3", 1},
		{"0.7.0", "0.9.3", -1},

		// Multi-digit components (the key bug).
		{"0.9.8", "0.10.0", -1},
		{"0.10.0", "0.9.8", 1},
		{"0.10.1", "0.10.2", -1},
		{"1.0.0", "0.99.99", 1},

		// Date-based versions.
		{"2025.06.02-133859", "2025.06.02-140000", -1},
		{"2025.06.03-100000", "2025.06.02-235959", 1},

		// With v prefix.
		{"v0.10.0", "v0.9.8", 1},
		{"v1.0.0", "0.99.0", 1},

		// Different lengths.
		{"1.0", "1.0.0", -1}, // "1.0" has fewer parts, missing part treated as ""
		{"1.0.0", "1.0", 1},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestIsDev(t *testing.T) {
	tests := []struct {
		v    string
		want bool
	}{
		{"dev", true},                   // local default build
		{"2026.06.30-133859-dev", true}, // CI branch build
		{"0.10.0-dev", true},
		{"0.10.0", false},
		{"2026.06.30-133859", false}, // main-branch CI build
		{"v1.0.0", false},
		{"", false},
		{"development", false}, // not the literal "dev" and no "-dev" suffix
		{"dev-2", false},       // "dev" only counts as an exact match or suffix
	}

	for _, tt := range tests {
		if got := IsDev(tt.v); got != tt.want {
			t.Errorf("IsDev(%q) = %v, want %v", tt.v, got, tt.want)
		}
	}
}
