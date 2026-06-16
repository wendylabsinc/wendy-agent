//go:build darwin || linux || windows

package commands

import "testing"

func TestManifestStorage(t *testing.T) {
	tests := []struct {
		name     string
		d        drive
		override string
		want     string
	}{
		{
			name:     "override nvme wins regardless of storage type",
			d:        drive{StorageType: StorageUnknown},
			override: "nvme",
			want:     "nvme",
		},
		{
			name:     "override sd wins regardless of storage type",
			d:        drive{StorageType: StorageNVMe},
			override: "sd",
			want:     "sd",
		},
		{
			name:     "USB-attached drive defaults to nvme",
			d:        drive{StorageType: StorageUSB},
			override: "",
			want:     "nvme",
		},
		{
			name:     "NVMe drive without override returns nvme",
			d:        drive{StorageType: StorageNVMe},
			override: "",
			want:     "nvme",
		},
		{
			name:     "unknown storage type returns sd",
			d:        drive{StorageType: StorageUnknown},
			override: "",
			want:     "sd",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := manifestStorage(tc.d, tc.override)
			if got != tc.want {
				t.Errorf("manifestStorage(%+v, %q) = %q; want %q", tc.d, tc.override, got, tc.want)
			}
		})
	}
}
