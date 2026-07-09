package main

import (
	"os"
	"testing"
)

func TestSimOnlyMode(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  bool
	}{
		{name: "unset", set: false, want: false},
		{name: "empty", set: true, value: "", want: false},
		{name: "one", set: true, value: "1", want: true},
		{name: "true", set: true, value: "true", want: true},
		{name: "TRUE", set: true, value: "TRUE", want: true},
		{name: "zero", set: true, value: "0", want: false},
		{name: "false", set: true, value: "false", want: false},
		{name: "garbage", set: true, value: "yes-please", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv registers cleanup restoring the original value, so it is
			// safe to follow it with os.Unsetenv for the "unset" case.
			t.Setenv(simOnlyEnvVar, tt.value)
			if !tt.set {
				if err := os.Unsetenv(simOnlyEnvVar); err != nil {
					t.Fatalf("Unsetenv: %v", err)
				}
			}
			if got := simOnlyMode(); got != tt.want {
				t.Errorf("simOnlyMode() = %v, want %v (env set=%v value=%q)", got, tt.want, tt.set, tt.value)
			}
		})
	}
}
