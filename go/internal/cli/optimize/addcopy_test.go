package optimize

import "testing"

func TestAddCopyFlagsLocalFile(t *testing.T) {
	tg := dockerfileTarget(t, "FROM alpine:3\nADD app.py /app/app.py\n")
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Analyzer != "add-copy" || f.Severity != SeverityInfo {
		t.Fatalf("finding = %+v", f)
	}
	if f.Fix == nil || f.Fix.New != "COPY app.py /app/app.py" {
		t.Fatalf("fix = %+v", f.Fix)
	}
}

func TestAddCopySilentOnURL(t *testing.T) {
	tg := dockerfileTarget(t, "FROM alpine:3\nADD https://example.com/app.tar.gz /app/\n")
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestAddCopySilentOnLocalArchive(t *testing.T) {
	tg := dockerfileTarget(t, "FROM alpine:3\nADD bundle.tar.gz /app/\n")
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestAddCopyIgnoresNonDockerTarget(t *testing.T) {
	tg := &Target{Name: "app", Kind: KindNativeSwift, Arch: "arm64"}
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0", len(got))
	}
}
