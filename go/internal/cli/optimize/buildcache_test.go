package optimize

import (
	"strings"
	"testing"
)

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

func TestBuildCacheReportsButNeverFixesPipNoCacheDir(t *testing.T) {
	// A pip cache mount next to --no-cache-dir would be dead weight (pip
	// ignores the mounted cache), and removing the user's flag isn't
	// additive — so the finding must carry no Fix.
	tg := dockerfileTarget(t, "FROM python:3.11-slim\nRUN pip install --no-cache-dir -r requirements.txt\n")
	got := buildCacheAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Fix != nil {
		t.Fatalf("expected report-only finding (no Fix), got %+v", got[0].Fix)
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

func TestBuildCacheManagerSpecificCacheDirs(t *testing.T) {
	cases := []struct{ line, target string }{
		{"RUN pip3 install -r requirements.txt", "/root/.cache/pip"},
		{"RUN yarn install", "/root/.cache/yarn"},
		{"RUN pnpm install", "/root/.local/share/pnpm/store"},
		{"RUN npm ci", "/root/.npm"},
	}
	for _, c := range cases {
		tg := dockerfileTarget(t, "FROM node:22\n"+c.line+"\n")
		got := buildCacheAnalyzer{}.Analyze(tg)
		if len(got) != 1 || got[0].Fix == nil {
			t.Fatalf("%s: got %+v, want one finding with fix", c.line, got)
		}
		want := "--mount=type=cache,target=" + c.target
		if !strings.Contains(got[0].Fix.New, want) {
			t.Fatalf("%s: fix %q missing %q", c.line, got[0].Fix.New, want)
		}
	}
}
