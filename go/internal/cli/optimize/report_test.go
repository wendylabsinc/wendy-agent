package optimize

import (
	"strings"
	"testing"
)

func sampleReport() Report {
	targets := []Target{{Name: "app", Kind: KindDockerfile}}
	findings := []Finding{
		{Analyzer: "arch-image", Target: "app", Severity: SeverityError, Title: "amd64 base", Location: &Loc{File: "Dockerfile", Line: 1}},
		{Analyzer: "build-cache", Target: "app", Severity: SeverityWarning, Title: "no cache", Location: &Loc{File: "Dockerfile", Line: 4}, Fix: &Fix{Op: FixReplaceLine}},
		{Analyzer: "arch-image", Target: "app", Severity: SeverityWarning, Title: "No .dockerignore", Fix: &Fix{Op: FixCreateFile}},
	}
	return BuildReport(targets, findings)
}

func TestCountsAndMaxSeverity(t *testing.T) {
	r := sampleReport()
	info, warn, errc, fixable := r.Counts()
	if info != 0 || warn != 2 || errc != 1 || fixable != 2 {
		t.Fatalf("counts = info:%d warn:%d err:%d fixable:%d", info, warn, errc, fixable)
	}
	if r.MaxSeverity() != SeverityError {
		t.Fatalf("MaxSeverity = %v, want error", r.MaxSeverity())
	}
}

func TestRenderHuman(t *testing.T) {
	out := RenderHuman(sampleReport())
	if !strings.Contains(out, "app (dockerfile)") {
		t.Fatalf("missing target header:\n%s", out)
	}
	if !strings.Contains(out, "build-cache:4") || !strings.Contains(out, "(fixable)") {
		t.Fatalf("missing fixable build-cache line:\n%s", out)
	}
	if strings.Contains(out, "arch-image:0") {
		t.Fatalf("location-less finding should not print :0\n%s", out)
	}
	if !strings.Contains(out, "No .dockerignore") {
		t.Fatalf("location-less finding missing from output:\n%s", out)
	}
}

func TestRenderHumanGroupsByTarget(t *testing.T) {
	targets := []Target{
		{Name: "api", Kind: KindComposeService},
		{Name: "web", Kind: KindComposeService},
	}
	findings := []Finding{
		{Analyzer: "build-cache", Target: "web", Severity: SeverityWarning, Title: "web cache", Location: &Loc{File: "web/Dockerfile", Line: 3}},
		{Analyzer: "build-cache", Target: "api", Severity: SeverityWarning, Title: "api cache", Location: &Loc{File: "api/Dockerfile", Line: 3}},
	}
	out := RenderHuman(BuildReport(targets, findings))
	apiIdx := strings.Index(out, "api cache")
	webIdx := strings.Index(out, "web cache")
	apiHdr := strings.Index(out, "api (compose-service)")
	webHdr := strings.Index(out, "web (compose-service)")
	if apiHdr < 0 || webHdr < 0 || apiIdx < 0 || webIdx < 0 {
		t.Fatalf("missing headers/findings:\n%s", out)
	}
	// api finding must appear under the api header (before the web header), web finding after the web header.
	if !(apiHdr < apiIdx && apiIdx < webHdr && webHdr < webIdx) {
		t.Fatalf("findings not grouped under their target headers:\n%s", out)
	}
}
