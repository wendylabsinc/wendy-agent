package optimize

import "testing"

func dockerfileTarget(t *testing.T, src string) *Target {
	t.Helper()
	df := ParseDockerfile("Dockerfile", []byte(src))
	return &Target{Name: "app", Kind: KindDockerfile, Dir: ".", Dockerfile: df, Arch: "arm64"}
}

func TestBuildCacheFlagsMissingMount(t *testing.T) {
	tg := dockerfileTarget(t, "FROM rust:1\nRUN cargo build --release\n")
	got := buildCacheAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Analyzer != "build-cache" || f.Severity != SeverityWarning {
		t.Fatalf("finding = %+v", f)
	}
	if f.Fix == nil || f.Fix.Op != FixReplaceLine {
		t.Fatalf("expected FixReplaceLine, got %+v", f.Fix)
	}
	if f.Fix.Old != "RUN cargo build --release" {
		t.Fatalf("fix.Old = %q", f.Fix.Old)
	}
	if f.Fix.New != "RUN --mount=type=cache,target=/root/.cargo cargo build --release" {
		t.Fatalf("fix.New = %q", f.Fix.New)
	}
}

func TestBuildCacheSilentWhenMountPresent(t *testing.T) {
	tg := dockerfileTarget(t, "FROM rust:1\nRUN --mount=type=cache,target=/root/.cargo cargo build\n")
	got := buildCacheAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestBuildCacheIgnoresNonDockerTarget(t *testing.T) {
	tg := &Target{Name: "app", Kind: KindNativeSwift, Arch: "arm64"}
	got := buildCacheAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0", len(got))
	}
}
