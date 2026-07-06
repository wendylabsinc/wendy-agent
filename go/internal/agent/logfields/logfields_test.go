package logfields

import "testing"

func TestKeysAreSnakeCaseAndStable(t *testing.T) {
	cases := map[string]string{
		"AppID":         AppID,
		"AppName":       AppName,
		"ContainerID":   ContainerID,
		"ContainerName": ContainerName,
		"ServiceName":   ServiceName,
	}
	want := map[string]string{
		"AppID":         "app_id",
		"AppName":       "app_name",
		"ContainerID":   "container_id",
		"ContainerName": "container_name",
		"ServiceName":   "service_name",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("%s = %q, want %q", name, got, want[name])
		}
	}
}
