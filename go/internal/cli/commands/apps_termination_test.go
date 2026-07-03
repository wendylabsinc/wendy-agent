package commands

import "testing"

func TestTerminationSummary(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		code   int32
		want   string
	}{
		{"running/unknown", "", 0, ""},
		{"crashed shows code", "crashed", 2, "crashed (exit 2)"},
		{"oom", "oom_killed", 137, "OOM killed"},
		{"start failed", "start_failed", -1, "start failed"},
		{"entitlement", "entitlement_denied", -1, "entitlement denied"},
		{"clean exit", "exited", 0, "exited"},
		{"unknown reason passes through", "weird_future_reason", 0, "weird_future_reason"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminationSummary(tc.reason, tc.code); got != tc.want {
				t.Fatalf("terminationSummary(%q, %d) = %q, want %q", tc.reason, tc.code, got, tc.want)
			}
		})
	}
}
