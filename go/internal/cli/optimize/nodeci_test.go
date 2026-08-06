package optimize

import (
	"os"
	"path/filepath"
	"testing"
)

func dockerfileTargetInDir(t *testing.T, dir, src string) *Target {
	t.Helper()
	df := ParseDockerfile(filepath.Join(dir, "Dockerfile"), []byte(src))
	return &Target{Name: "app", Kind: KindDockerfile, Dir: dir, Dockerfile: df, Arch: "arm64"}
}

func TestNodeCIFlagsNpmInstallWithLockfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	tg := dockerfileTargetInDir(t, dir, "FROM node:20\nRUN npm install\n")
	got := nodeCIAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Analyzer != "node-ci" || f.Severity != SeverityWarning {
		t.Fatalf("finding = %+v", f)
	}
	if f.Fix != nil {
		t.Fatalf("expected no auto-fix (npm ci can hard-fail on a drifted lockfile — report-only), got %+v", f.Fix)
	}
}

func TestNodeCISilentWithoutLockfile(t *testing.T) {
	dir := t.TempDir()
	tg := dockerfileTargetInDir(t, dir, "FROM node:20\nRUN npm install\n")
	got := nodeCIAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestNodeCISilentWhenAlreadyCI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	tg := dockerfileTargetInDir(t, dir, "FROM node:20\nRUN npm ci\n")
	got := nodeCIAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestNodeCISilentOnGlobalInstall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	tg := dockerfileTargetInDir(t, dir, "FROM node:20\nRUN npm install -g pnpm\n")
	got := nodeCIAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestNodeCIIgnoresNonDockerTarget(t *testing.T) {
	tg := &Target{Name: "app", Kind: KindNativeSwift, Arch: "arm64"}
	got := nodeCIAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0", len(got))
	}
}
