package optimize

import "testing"

type fakeAnalyzer struct{ id string }

func (f fakeAnalyzer) ID() string { return f.id }
func (f fakeAnalyzer) Analyze(t *Target) []Finding {
	return []Finding{{Analyzer: f.id, Severity: SeverityWarning, Title: t.Name}}
}

func TestAnalyzeRunsAllAnalyzersOverAllTargets(t *testing.T) {
	targets := []Target{{Name: "a", Kind: KindDockerfile}, {Name: "b", Kind: KindDockerfile}}
	analyzers := []Analyzer{fakeAnalyzer{"x"}, fakeAnalyzer{"y"}}

	findings := Analyze(targets, analyzers)
	if len(findings) != 4 {
		t.Fatalf("got %d findings, want 4", len(findings))
	}
	// target-then-analyzer order
	if findings[0].Title != "a" || findings[0].Analyzer != "x" {
		t.Fatalf("findings[0] = %+v", findings[0])
	}
	if findings[1].Analyzer != "y" {
		t.Fatalf("findings[1] = %+v", findings[1])
	}
	if findings[2].Title != "b" {
		t.Fatalf("findings[2] = %+v", findings[2])
	}
}
