package optimize

import "testing"

func TestReleaseDebugSwiftMissingReleaseFlag(t *testing.T) {
	tg := dockerfileTarget(t, "FROM swift:6\nRUN swift build\n")
	got := releaseDebugAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1: %+v", len(got), got)
	}
	if got[0].Fix == nil {
		t.Fatalf("expected fix, got nil")
	}
	if got[0].Fix.New != "RUN swift build -c release" {
		t.Fatalf("fix.New = %q, want %q", got[0].Fix.New, "RUN swift build -c release")
	}
}

func TestReleaseDebugSwiftSilentWithReleaseFlag(t *testing.T) {
	tg := dockerfileTarget(t, "FROM swift:6\nRUN swift build -c release\n")
	got := releaseDebugAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d, want 0: %+v", len(got), got)
	}
}

func TestReleaseDebugWendyDebugDefinedButUnused(t *testing.T) {
	tg := dockerfileTarget(t, "FROM swift:6\nARG WENDY_DEBUG=0\nRUN swift build -c release\n")
	got := releaseDebugAnalyzer{}.Analyze(tg)
	if len(got) != 1 || got[0].Severity != SeverityInfo {
		t.Fatalf("want 1 info finding for WENDY_DEBUG, got %+v", got)
	}
	if got[0].Fix != nil {
		t.Fatalf("WENDY_DEBUG finding should be report-only")
	}
}

func TestReleaseDebugWendyDebugUsed(t *testing.T) {
	src := "FROM swift:6\nARG WENDY_DEBUG=0\nRUN if [ \"$WENDY_DEBUG\" = \"1\" ]; then swift build; else swift build -c release; fi\n"
	tg := dockerfileTarget(t, src)
	got := releaseDebugAnalyzer{}.Analyze(tg)
	for _, f := range got {
		if f.Severity == SeverityInfo {
			t.Fatalf("did not expect WENDY_DEBUG info finding: %+v", f)
		}
	}
}
