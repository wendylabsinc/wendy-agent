package mcp

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestEveryToolHasTitleAndHints(t *testing.T) {
	srv, err := New(&config.Config{}, nil).newProtocolServer()
	if err != nil {
		t.Fatal(err)
	}
	tools := srv.ListTools()
	for name, st := range tools {
		a := st.Tool.Annotations
		if st.Tool.Title == "" || a.Title == "" {
			t.Errorf("%s has no title; add it to toolTitles", name)
		}
		if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
			t.Errorf("%s lacks an explicit hint: %+v", name, a)
		}
	}
	for name := range toolTitles {
		if _, ok := tools[name]; !ok {
			t.Errorf("toolTitles has %s, which is not registered", name)
		}
	}
}

func TestManifestVersion(t *testing.T) {
	a, err := Manifest()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if a.Version != b.Version || len(a.Tools) == 0 {
		t.Fatalf("manifest not stable: %s vs %s (%d tools)", a.Version, b.Version, len(a.Tools))
	}
	if err := CheckRelayManifest("0.0.1+" + manifestHash(a.Version)); err != nil {
		t.Errorf("same tool set from another CLI release refused: %v", err)
	}
	for _, v := range []string{"", a.Version + "0", "0.0.1+deadbeefdeadbeef"} {
		if CheckRelayManifest(v) == nil {
			t.Errorf("relay manifest %q accepted", v)
		}
	}
}
