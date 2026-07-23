//go:build windows

package commands

import (
	"reflect"
	"testing"
)

func TestInjectElevationArgs(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		extra []string
		want  []string
	}{
		{
			"appends missing flags",
			[]string{"os", "install"},
			[]string{"--device-type=jetson-orin-nano", "--rootfs-only=true"},
			[]string{"os", "install", "--device-type=jetson-orin-nano", "--rootfs-only=true"},
		},
		{
			"skips flag already present in attached form",
			[]string{"os", "install", "--device-type=jetson-agx-orin"},
			[]string{"--device-type=jetson-orin-nano", "--rootfs-only=false"},
			[]string{"os", "install", "--device-type=jetson-agx-orin", "--rootfs-only=false"},
		},
		{
			"skips flag already present as bare token",
			[]string{"os", "install", "--rootfs-only"},
			[]string{"--rootfs-only=false"},
			[]string{"os", "install", "--rootfs-only"},
		},
		{
			"skips flag already present in detached form",
			[]string{"os", "install", "--device-type", "jetson-agx-orin"},
			[]string{"--device-type=jetson-orin-nano"},
			[]string{"os", "install", "--device-type", "jetson-agx-orin"},
		},
		{
			"no extras leaves args unchanged",
			[]string{"os", "install", "--nightly"},
			nil,
			[]string{"os", "install", "--nightly"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectElevationArgs(tt.args, tt.extra)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("injectElevationArgs(%v, %v) = %v, want %v", tt.args, tt.extra, got, tt.want)
			}
		})
	}
}
