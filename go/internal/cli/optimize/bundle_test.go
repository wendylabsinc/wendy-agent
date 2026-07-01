package optimize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func TestBuildBundle(t *testing.T) {
	df := ParseDockerfile("Dockerfile", []byte("FROM rust:1\nRUN cargo build\n"))
	targets := []Target{{
		Name:       "app",
		Kind:       KindDockerfile,
		Dir:        ".",
		Dockerfile: df,
		Arch:       "arm64",
		Config:     &appconfig.AppConfig{AppID: "demo", Platform: "wendyos"},
	}}
	findings := []Finding{{Analyzer: "build-cache", Severity: SeverityWarning, Title: "no cache"}}

	b := BuildBundle(".", "{\"appId\":\"demo\"}", targets, findings)

	if b.Schema != 1 {
		t.Fatalf("schema = %d, want 1", b.Schema)
	}
	if b.Project.AppID != "demo" || b.Project.Arch != "arm64" {
		t.Fatalf("project = %+v", b.Project)
	}
	if len(b.Targets) != 1 || !strings.Contains(b.Targets[0].Dockerfile, "cargo build") {
		t.Fatalf("targets = %+v", b.Targets)
	}
	if b.Targets[0].RequirementsTxt != nil {
		t.Fatalf("expected nil RequirementsTxt")
	}
	if len(b.StaticFindings) != 1 || b.Instructions == "" {
		t.Fatalf("findings/instructions missing")
	}

	// Must round-trip through JSON.
	if _, err := json.Marshal(b); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}
