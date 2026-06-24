//go:build linux

package bluetooth

import "testing"

func TestDeviceObjectPath(t *testing.T) {
	tests := []struct {
		name        string
		adapterPath string
		address     string
		want        string
	}{
		{
			name:        "standard uppercase address",
			adapterPath: "/org/bluez/hci0",
			address:     "AA:BB:CC:DD:EE:FF",
			want:        "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF",
		},
		{
			name:        "lowercase address is upper-cased",
			adapterPath: "/org/bluez/hci0",
			address:     "a1:b2:c3:d4:e5:f6",
			want:        "/org/bluez/hci0/dev_A1_B2_C3_D4_E5_F6",
		},
		{
			name:        "non-default adapter path",
			adapterPath: "/org/bluez/hci1",
			address:     "00:11:22:33:44:55",
			want:        "/org/bluez/hci1/dev_00_11_22_33_44_55",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(deviceObjectPath(tt.adapterPath, tt.address)); got != tt.want {
				t.Errorf("deviceObjectPath(%q, %q) = %q, want %q", tt.adapterPath, tt.address, got, tt.want)
			}
		})
	}
}
