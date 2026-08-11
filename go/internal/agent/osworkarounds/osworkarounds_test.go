package osworkarounds

import "testing"

// TestForCleanRebootForCapsuleDurability pins the version boundary of the
// WDY-2200 workaround at 0.18.1, the release carrying wendyos-update cb2c7b5.
//
// The fail-open cases matter as much as the affected ones: a dev, empty, or
// unparseable version must NOT be treated as affected, so a development or CI
// image never silently takes a different reboot path than the one it was tested
// with.
func TestForCleanRebootForCapsuleDurability(t *testing.T) {
	tests := []struct {
		name      string
		osVersion string
		want      bool
	}{
		{"0.16.1 predates the fix", "0.16.1", true},
		{"0.17.0 predates the fix", "0.17.0", true},
		{"0.18.0 predates the fix", "0.18.0", true},
		{"the WendyOS- display prefix is stripped", "WendyOS-0.17.0", true},
		{"a nightly below the fix is affected", "0.17.0-nightly", true},
		{"a two-component version below the fix is affected", "0.18", true},

		{"0.18.1 carries the fix", "0.18.1", false},
		{"a nightly at the fix is not affected", "0.18.1-nightly", false},
		{"0.18.2 carries the fix", "0.18.2", false},
		{"WendyOS-0.18.2 carries the fix", "WendyOS-0.18.2", false},
		{"a later minor carries the fix", "0.19.0", false},
		{"a later major carries the fix", "1.0.0", false},

		{"an empty version fails open", "", false},
		{"a dev build fails open", "dev", false},
		{"a dev-suffixed build fails open", "2026.06.30-133859-dev", false},
		{"an unparseable version fails open", "garbage", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := For(tc.osVersion).CleanRebootForCapsuleDurability; got != tc.want {
				t.Errorf("For(%q).CleanRebootForCapsuleDurability = %v, want %v", tc.osVersion, got, tc.want)
			}
		})
	}
}

// TestForIsZeroForUnaffectedVersions guards the shape of the type rather than a
// single field: a current OS must opt into no workarounds at all, so adding a
// field later cannot accidentally default to "on" for a healthy image.
func TestForIsZeroForUnaffectedVersions(t *testing.T) {
	for _, v := range []string{"0.18.1", "1.0.0", "dev", ""} {
		if got := For(v); got != (Set{}) {
			t.Errorf("For(%q) = %+v, want the zero Set", v, got)
		}
	}
}
