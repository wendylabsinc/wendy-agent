package main

import "testing"

func TestCNIPluginName(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		argv0Base string
		want      string
	}{
		{
			name:      "argv0 bridge",
			args:      nil,
			argv0Base: "bridge",
			want:      "bridge",
		},
		{
			name:      "argv0 host-local",
			args:      nil,
			argv0Base: "host-local",
			want:      "host-local",
		},
		{
			name:      "explicit cni bridge subcommand",
			args:      []string{"cni", "bridge"},
			argv0Base: "wendy-agent",
			want:      "bridge",
		},
		{
			name:      "explicit cni host-local subcommand",
			args:      []string{"cni", "host-local"},
			argv0Base: "wendy-agent",
			want:      "host-local",
		},
		{
			name:      "not a CNI invocation",
			args:      []string{"utils", "open-browser", "http://x"},
			argv0Base: "wendy-agent",
			want:      "",
		},
		{
			name:      "no args, ordinary argv0",
			args:      nil,
			argv0Base: "wendy-agent",
			want:      "",
		},
		{
			name:      "cni with unknown plugin name",
			args:      []string{"cni", "unknown"},
			argv0Base: "wendy-agent",
			want:      "",
		},
		{
			name:      "cni with missing plugin arg",
			args:      []string{"cni"},
			argv0Base: "wendy-agent",
			want:      "",
		},
		{
			name:      "argv0 takes precedence over args",
			args:      []string{"utils", "open-browser"},
			argv0Base: "bridge",
			want:      "bridge",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cniPluginName(tt.args, tt.argv0Base); got != tt.want {
				t.Errorf("cniPluginName(%v, %q) = %q, want %q", tt.args, tt.argv0Base, got, tt.want)
			}
		})
	}
}
