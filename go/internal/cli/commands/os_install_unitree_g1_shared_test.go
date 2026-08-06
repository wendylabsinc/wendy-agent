package commands

import "testing"

func TestUnitreeG1DriveMeetsMinimumCapacity(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want bool
	}{
		{name: "one byte below one terabyte", size: 999_999_999_999, want: false},
		{name: "one terabyte", size: 1_000_000_000_000, want: true},
		{name: "larger drive", size: 2_000_000_000_000, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unitreeG1DriveMeetsMinimumCapacity(tc.size); got != tc.want {
				t.Fatalf("unitreeG1DriveMeetsMinimumCapacity(%d) = %v, want %v", tc.size, got, tc.want)
			}
		})
	}
}
