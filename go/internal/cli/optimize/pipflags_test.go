package optimize

import "testing"

func TestPipFlagsMissingNoCacheDir(t *testing.T) {
	tg := dockerfileTarget(t, "FROM python:3.12\nRUN pip install -r requirements.txt\n")
	got := pipFlagsAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Analyzer != "pip-flags" || f.Severity != SeverityWarning {
		t.Fatalf("finding = %+v", f)
	}
	if f.Fix == nil || f.Fix.Op != FixReplaceLine {
		t.Fatalf("expected FixReplaceLine, got %+v", f.Fix)
	}
	if f.Fix.New != "RUN pip install --no-cache-dir -r requirements.txt" {
		t.Fatalf("fix.New = %q", f.Fix.New)
	}
}

func TestPipFlagsSilentWhenPresent(t *testing.T) {
	tg := dockerfileTarget(t, "FROM python:3.12\nRUN pip install --no-cache-dir -r requirements.txt\n")
	got := pipFlagsAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestPipFlagsMatchesPip3(t *testing.T) {
	tg := dockerfileTarget(t, "FROM python:3.12\nRUN pip3 install numpy\n")
	got := pipFlagsAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Fix.New != "RUN pip3 install --no-cache-dir numpy" {
		t.Fatalf("fix.New = %q", got[0].Fix.New)
	}
}

func TestPipFlagsIgnoresNonDockerTarget(t *testing.T) {
	tg := &Target{Name: "app", Kind: KindNativeSwift, Arch: "arm64"}
	got := pipFlagsAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0", len(got))
	}
}
