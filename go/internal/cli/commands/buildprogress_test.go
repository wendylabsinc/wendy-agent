package commands

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunBuildWithProgressPlainSuccess(t *testing.T) {
	// Force non-interactive rendering and capture stdout via the package sink.
	restore := forceBuildProgressInteractive(false)
	defer restore()
	var out strings.Builder
	restoreOut := setBuildProgressOut(&out)
	defer restoreOut()

	err := runBuildWithProgress(context.Background(), "Building image...", dumpRawAlways, func(stream, logw io.Writer) error {
		io.WriteString(stream, "#9 [4/6] RUN pip install\n#9 DONE 4.3s\n")
		io.WriteString(stream, "#6 [1/6] FROM python\n#6 CACHED\n")
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "cached") || !strings.Contains(got, "4.3s") {
		t.Errorf("missing step lines:\n%s", got)
	}
	if !strings.Contains(got, "1 cached") || !strings.Contains(got, "1 rebuilt") {
		t.Errorf("missing summary tally:\n%s", got)
	}
}

func TestRunBuildWithProgressPrintsRawOnFailure(t *testing.T) {
	restore := forceBuildProgressInteractive(false)
	defer restore()
	var out strings.Builder
	restoreOut := setBuildProgressOut(&out)
	defer restoreOut()

	wantErr := errors.New("docker buildx build failed")
	err := runBuildWithProgress(context.Background(), "Building image...", dumpRawAlways, func(stream, logw io.Writer) error {
		io.WriteString(stream, "#9 [4/6] RUN pip install\n")
		io.WriteString(stream, "#9 12.34 ERROR: could not find a version\n")
		io.WriteString(logw, "[buildx] bootstrapping builder\n")
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	got := out.String()
	// Raw build output AND setup log are surfaced on failure.
	if !strings.Contains(got, "could not find a version") {
		t.Errorf("raw build output not surfaced:\n%s", got)
	}
	if !strings.Contains(got, "bootstrapping builder") {
		t.Errorf("setup log not surfaced on failure:\n%s", got)
	}
}

func TestRunBuildWithProgressSuppressesRawOnFailureWhenDumpDisabled(t *testing.T) {
	restore := forceBuildProgressInteractive(false)
	defer restore()
	var out strings.Builder
	restoreOut := setBuildProgressOut(&out)
	defer restoreOut()

	wantErr := errors.New("oci layout build failed")
	err := runBuildWithProgress(context.Background(), "Building image (OCI layout)...", func(error) bool { return false }, func(stream, logw io.Writer) error {
		io.WriteString(stream, "#5 [3/5] RUN apt-get install\n")
		io.WriteString(stream, "#5 12.34 ERROR: package not found\n")
		io.WriteString(logw, "[buildx] starting builder instance\n")
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	got := out.String()
	// With dumpRawOnFailure=false, raw build output and setup log must NOT appear.
	if strings.Contains(got, "package not found") {
		t.Errorf("raw build output should be suppressed when dumpRawOnFailure=false, but got:\n%s", got)
	}
	if strings.Contains(got, "starting builder instance") {
		t.Errorf("setup log should be suppressed when dumpRawOnFailure=false, but got:\n%s", got)
	}
}

// Regression test for WDY-1813: an apple-container (or buildx) image-build
// failure on the default chunk-diff path is surfaced directly to the user
// without a registry-push fallback, so the captured build log must be dumped —
// previously it was discarded and the user saw only the ✗ line.
func TestChunkDiffBuildLogDumpedForImageBuildFailureUnderAutoChunking(t *testing.T) {
	restore := forceBuildProgressInteractive(false)
	defer restore()
	var out strings.Builder
	restoreOut := setBuildProgressOut(&out)
	defer restoreOut()

	wantErr := &imageBuildFailedError{errors.New("container build (OCI layout) failed: exit status 1")}
	err := runBuildWithProgress(context.Background(), "Building image (OCI layout)...", shouldDumpChunkDiffBuildLog(chunkingAuto), func(stream, logw io.Writer) error {
		io.WriteString(stream, "#5 [3/5] COPY Package.swift .\n")
		io.WriteString(stream, "#5 ERROR: failed to compute cache key: \"/Package.swift\": not found\n")
		io.WriteString(logw, "[apple-container] building OCI image: container build --progress plain ...\n")
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	got := out.String()
	if !strings.Contains(got, "failed to compute cache key") {
		t.Errorf("raw build output not surfaced on image-build failure:\n%s", got)
	}
	// The setup log carries the exact builder command line for manual reproduction.
	if !strings.Contains(got, "building OCI image: container build") {
		t.Errorf("builder command line not surfaced on image-build failure:\n%s", got)
	}
}

func TestAppleContainerContextMonitorDiagnosesEmptyTmpTransfer(t *testing.T) {
	m := &appleContainerBuildContextMonitor{
		contextPath: "/tmp/ctxprobe",
		pathInTmp:   true,
		stats:       appleContainerContextStats{fileCount: 2},
	}
	if _, err := m.Write([]byte("#4 [internal] load build context\n#4 transferring context: 2")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write([]byte("B\n")); err != nil {
		t.Fatal(err)
	}

	diagnosis := m.diagnosis()
	for _, want := range []string{"transferred an empty build context", "known apple-container issue", "non-/tmp directory"} {
		if !strings.Contains(diagnosis, want) {
			t.Fatalf("diagnosis missing %q:\n%s", want, diagnosis)
		}
	}
}

func TestAppleContainerContextMonitorIgnoresNonTmpOrNonEmptyTransfer(t *testing.T) {
	cases := []struct {
		name string
		m    appleContainerBuildContextMonitor
		line string
	}{
		{
			name: "non tmp path",
			m:    appleContainerBuildContextMonitor{contextPath: "/Users/me/app", pathInTmp: false, stats: appleContainerContextStats{fileCount: 2}},
			line: "#4 transferring context: 2B\n",
		},
		{
			name: "normal context transfer",
			m:    appleContainerBuildContextMonitor{contextPath: "/tmp/app", pathInTmp: true, stats: appleContainerContextStats{fileCount: 2}},
			line: "#4 transferring context: 40B\n",
		},
		{
			name: "empty local project",
			m:    appleContainerBuildContextMonitor{contextPath: "/tmp/app", pathInTmp: true, stats: appleContainerContextStats{}},
			line: "#4 transferring context: 2B\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.m.Write([]byte(tc.line)); err != nil {
				t.Fatal(err)
			}
			if got := tc.m.diagnosis(); got != "" {
				t.Fatalf("diagnosis = %q, want empty", got)
			}
		})
	}
}

func TestShouldDumpChunkDiffBuildLog(t *testing.T) {
	buildErr := &imageBuildFailedError{errors.New("boom")}
	setupErr := errors.New("creating buildx builder: boom")
	cases := []struct {
		chunking string
		err      error
		want     bool
	}{
		{chunkingAuto, buildErr, true},  // surfaced directly (#1166) → dump
		{chunkingAuto, setupErr, false}, // falls back to registry push → quiet
		{chunkingForce, buildErr, true}, // no fallback → dump
		{chunkingForce, setupErr, true}, // no fallback → dump
	}
	for _, c := range cases {
		if got := shouldDumpChunkDiffBuildLog(c.chunking)(c.err); got != c.want {
			t.Errorf("shouldDumpChunkDiffBuildLog(%q)(%v) = %v, want %v", c.chunking, c.err, got, c.want)
		}
	}
}
