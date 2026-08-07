package optimize

import "testing"

func TestAptInstallFlagsMissingNoInstallRecommends(t *testing.T) {
	tg := dockerfileTarget(t, "FROM debian:12\nRUN apt-get update && apt-get install -y curl\n")
	got := aptInstallAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Analyzer != "apt-install" || f.Severity != SeverityWarning {
		t.Fatalf("finding = %+v", f)
	}
	if f.Fix == nil || f.Fix.Op != FixReplaceLine {
		t.Fatalf("expected FixReplaceLine, got %+v", f.Fix)
	}
	if f.Fix.New != "RUN apt-get update && apt-get install --no-install-recommends -y curl" {
		t.Fatalf("fix.New = %q", f.Fix.New)
	}
}

func TestAptInstallFlagsSplitAcrossLayers(t *testing.T) {
	tg := dockerfileTarget(t, "FROM debian:12\nRUN apt-get update\nRUN apt-get install --no-install-recommends -y curl\n")
	got := aptInstallAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Analyzer != "apt-install" || f.Severity != SeverityWarning {
		t.Fatalf("finding = %+v", f)
	}
	if f.Fix != nil {
		t.Fatalf("expected no fix (structural change), got %+v", f.Fix)
	}
	if f.Location == nil || f.Location.Line != 3 {
		t.Fatalf("location = %+v, want line 3", f.Location)
	}
}

func TestAptInstallSilentWhenCombinedAndFlagged(t *testing.T) {
	tg := dockerfileTarget(t, "FROM debian:12\nRUN apt-get update && apt-get install --no-install-recommends -y curl && rm -rf /var/lib/apt/lists/*\n")
	got := aptInstallAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestAptInstallIgnoresNonDockerTarget(t *testing.T) {
	tg := &Target{Name: "app", Kind: KindNativeSwift, Arch: "arm64"}
	got := aptInstallAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0", len(got))
	}
}
