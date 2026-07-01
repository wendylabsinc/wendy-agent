package optimize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchAmd64OnArm(t *testing.T) {
	tg := dockerfileTarget(t, "FROM --platform=linux/amd64 python:3.12-slim\n")
	tg.Dir = t.TempDir()
	writeFile(t, tg.Dir, ".dockerignore", ".git\n")
	got := archImageAnalyzer{}.Analyze(tg)
	var sawErr bool
	for _, f := range got {
		if f.Severity == SeverityError {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatalf("expected an error finding for amd64-on-arm, got %+v", got)
	}
}

func TestArchMissingDockerignoreFixable(t *testing.T) {
	tg := dockerfileTarget(t, "FROM python:3.12-slim\n")
	tg.Dir = t.TempDir()
	got := archImageAnalyzer{}.Analyze(tg)
	var fix *Fix
	for i := range got {
		if got[i].Title == "No .dockerignore" {
			fix = got[i].Fix
		}
	}
	if fix == nil || fix.Op != FixCreateFile {
		t.Fatalf("expected FixCreateFile for missing .dockerignore, got %+v", got)
	}
	if fix.File != filepath.Join(tg.Dir, ".dockerignore") {
		t.Fatalf("fix.File = %q", fix.File)
	}
}

func TestArchDockerignorePresentNoFinding(t *testing.T) {
	tg := dockerfileTarget(t, "FROM python:3.12-slim\n")
	tg.Dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(tg.Dir, ".dockerignore"), []byte(".git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	analyzer := archImageAnalyzer{}
	got := analyzer.Analyze(tg)
	for _, f := range got {
		if f.Title == "No .dockerignore" {
			t.Fatalf("did not expect a .dockerignore finding")
		}
	}
}
