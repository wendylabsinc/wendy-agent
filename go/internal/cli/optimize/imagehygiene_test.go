package optimize

import "testing"

func TestImageHygieneFlagsLatestTag(t *testing.T) {
	tg := dockerfileTarget(t, "FROM node:latest\n")
	got := imageHygieneAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Title != "FROM node:latest is not pinned to a version" {
		t.Fatalf("title = %q", got[0].Title)
	}
	if got[0].Fix != nil {
		t.Fatalf("expected no auto-fix (can't guess a version), got %+v", got[0].Fix)
	}
}

func TestImageHygieneFlagsUntaggedFrom(t *testing.T) {
	tg := dockerfileTarget(t, "FROM node\n")
	got := imageHygieneAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Title != "FROM node has no tag (defaults to :latest)" {
		t.Fatalf("title = %q", got[0].Title)
	}
}

func TestImageHygieneSilentOnPinnedFrom(t *testing.T) {
	tg := dockerfileTarget(t, "FROM node:20-slim\n")
	got := imageHygieneAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestImageHygieneSilentOnDigestPin(t *testing.T) {
	tg := dockerfileTarget(t, "FROM node@sha256:abcdef1234567890\n")
	got := imageHygieneAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestImageHygieneSilentOnBuildStageReference(t *testing.T) {
	// "FROM builder" refers to a prior named stage, not a registry image.
	tg := dockerfileTarget(t, "FROM golang:1.22 AS builder\nRUN go build ./...\nFROM builder\n")
	got := imageHygieneAnalyzer{}.Analyze(tg)
	for _, f := range got {
		if f.Location != nil && f.Location.Line == 3 {
			t.Fatalf("should not flag FROM referencing a named build stage: %+v", f)
		}
	}
}

func TestImageHygieneFlagsShellFormCMD(t *testing.T) {
	tg := dockerfileTarget(t, "FROM alpine:3\nCMD npm start\n")
	got := imageHygieneAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Title != "CMD uses shell form" {
		t.Fatalf("title = %q", got[0].Title)
	}
	if got[0].Fix != nil {
		t.Fatalf("expected no auto-fix (shell features may not translate), got %+v", got[0].Fix)
	}
}

func TestImageHygieneFlagsShellFormEntrypoint(t *testing.T) {
	tg := dockerfileTarget(t, "FROM alpine:3\nENTRYPOINT /app/run.sh\n")
	got := imageHygieneAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Title != "ENTRYPOINT uses shell form" {
		t.Fatalf("title = %q", got[0].Title)
	}
}

func TestImageHygieneSilentOnExecFormCMD(t *testing.T) {
	tg := dockerfileTarget(t, `FROM alpine:3
CMD ["npm", "start"]
`)
	got := imageHygieneAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestImageHygieneFlagsBroadCopyFrom(t *testing.T) {
	tg := dockerfileTarget(t, "FROM golang:1.22 AS builder\nRUN go build -o /bin/app ./...\nFROM alpine:3\nCOPY --from=builder / /\n")
	got := imageHygieneAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Title != "COPY --from copies the entire build stage" {
		t.Fatalf("title = %q", got[0].Title)
	}
}

func TestImageHygieneSilentOnNarrowCopyFrom(t *testing.T) {
	tg := dockerfileTarget(t, "FROM golang:1.22 AS builder\nRUN go build -o /bin/app ./...\nFROM alpine:3\nCOPY --from=builder /bin/app /bin/app\n")
	got := imageHygieneAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestImageHygieneIgnoresNonDockerTarget(t *testing.T) {
	tg := &Target{Name: "app", Kind: KindNativeSwift, Arch: "arm64"}
	got := imageHygieneAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0", len(got))
	}
}
