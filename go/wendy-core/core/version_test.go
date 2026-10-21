package core

import (
	"runtime/debug"
	"testing"
)

func TestModuleVersion(t *testing.T) {
	for _, test := range []struct {
		name       string
		dependency *debug.Module
		want       string
	}{
		{"stable", &debug.Module{Version: "v0.1.0"}, "0.1.0"},
		{"pseudo", &debug.Module{Version: "v0.0.0-20261021200000-123456789abc"}, "0.0.0-20261021200000-123456789abc"},
		{"preview", &debug.Module{Version: "v0.1.0-pr.3-kb-sdk-2.123.1"}, "0.1.0-pr.3-kb-sdk-2.123.1"},
		{"local replacement", &debug.Module{Version: "v0.0.1", Replace: &debug.Module{Path: "../core"}}, developmentVersion},
		{"versioned replacement", &debug.Module{Version: "v0.0.1", Replace: &debug.Module{Version: "v0.2.0"}}, "0.2.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.dependency.Path = "github.com/wendylabsinc/wendy-core"
			info := &debug.BuildInfo{Deps: []*debug.Module{{Path: "unrelated", Version: "v9.0.0"}, test.dependency}}
			if got := moduleVersion(info); got != test.want {
				t.Fatalf("Version = %q, want %q", got, test.want)
			}
		})
	}
	if got := moduleVersion(&debug.BuildInfo{}); got != developmentVersion {
		t.Fatal(got)
	}
}

func TestVersion(t *testing.T) {
	if got := Version(); got != developmentVersion {
		t.Fatalf("source version = %q", got)
	}
}
