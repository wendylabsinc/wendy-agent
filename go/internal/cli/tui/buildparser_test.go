package tui

import (
	"testing"
	"time"
)

// collect runs the given plain-progress text through the parser and returns the
// emitted events.
func collect(t *testing.T, text string) []BuildStepEvent {
	t.Helper()
	var got []BuildStepEvent
	p := NewBuildParser(func(e BuildStepEvent) { got = append(got, e) })
	if _, err := p.Write([]byte(text)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return got
}

func TestParserEmitsStepStartAndDoneWithDuration(t *testing.T) {
	text := "#9 [4/6] RUN pip install -r requirements.txt\n" +
		"#9 1.563 Collecting debugpy\n" +
		"#9 DONE 4.3s\n"
	got := collect(t, text)
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	if got[0].Status != BuildStepRunning || got[0].Kind != BuildVertexStep ||
		got[0].Display != "[4/6] RUN pip install -r requirements.txt" {
		t.Errorf("start event wrong: %+v", got[0])
	}
	if got[1].Status != BuildStepDone || got[1].Dur != 4300*time.Millisecond {
		t.Errorf("done event wrong: %+v (dur=%v)", got[1], got[1].Dur)
	}
}

func TestParserMarksCachedStep(t *testing.T) {
	text := "#6 [1/6] FROM docker.io/library/python:3.11-slim\n#6 CACHED\n"
	got := collect(t, text)
	if len(got) != 2 || got[1].Status != BuildStepCached {
		t.Fatalf("want a cached event, got %+v", got)
	}
}

func TestParserHidesInternalNoise(t *testing.T) {
	// .dockerignore / build context / cache manifest / resolve are noise.
	text := "#3 [internal] load .dockerignore\n#3 DONE 0.0s\n" +
		"#4 importing cache manifest from local:123\n#4 DONE 0.0s\n" +
		"#5 [internal] load build context\n#5 DONE 0.0s\n"
	if got := collect(t, text); len(got) != 0 {
		t.Fatalf("want no events for internal noise, got %+v", got)
	}
}

func TestParserShowsSetupSteps(t *testing.T) {
	text := "#1 [internal] load build definition from Dockerfile\n#1 DONE 0.0s\n" +
		"#2 [internal] load metadata for docker.io/library/python:3.11-slim\n#2 DONE 2.0s\n"
	got := collect(t, text)
	if len(got) != 4 {
		t.Fatalf("want 4 events, got %d: %+v", len(got), got)
	}
	if got[0].Kind != BuildVertexSetup || got[0].Display != "load build definition" {
		t.Errorf("event 0 wrong: %+v", got[0])
	}
	if got[2].Display != "load metadata" {
		t.Errorf("event 2 wrong: %+v", got[2])
	}
}

func TestParserCollapsesExportAndPushIntoOnePhase(t *testing.T) {
	text := "#12 exporting to image\n#12 exporting layers\n#12 DONE 0.5s\n" +
		"#13 exporting cache to client directory\n#13 DONE 0.3s\n" +
		"#12 pushing layers\n#12 DONE 12.6s\n"
	got := collect(t, text)
	// Exactly one Running export event, no per-vertex done/cached events.
	if len(got) != 1 {
		t.Fatalf("want 1 collapsed export event, got %d: %+v", len(got), got)
	}
	if got[0].ID != BuildExportVertexID || got[0].Kind != BuildVertexExport ||
		got[0].Status != BuildStepRunning || got[0].Display != "exporting + pushing layers" {
		t.Errorf("export event wrong: %+v", got[0])
	}
}

func TestParserMarksFailedStep(t *testing.T) {
	text := "#9 [4/6] RUN pip install -r requirements.txt\n" +
		"#9 ERROR: process \"/bin/sh -c pip install\" did not complete successfully\n"
	got := collect(t, text)
	if len(got) != 2 || got[1].Status != BuildStepFailed {
		t.Fatalf("want a failed event, got %+v", got)
	}
}

func TestParserHandlesSplitWrites(t *testing.T) {
	var got []BuildStepEvent
	p := NewBuildParser(func(e BuildStepEvent) { got = append(got, e) })
	p.Write([]byte("#9 [4/6] RUN pip insta"))
	p.Write([]byte("ll\n#9 DON"))
	p.Write([]byte("E 4.3s\n"))
	if len(got) != 2 || got[0].Status != BuildStepRunning || got[1].Status != BuildStepDone {
		t.Fatalf("split writes mis-parsed: %+v", got)
	}
}

func TestParserHidesInternalVertexWithFraction(t *testing.T) {
	text := "#7 [internal 2/3] settle layers\n#7 DONE 0.0s\n"
	if got := collect(t, text); len(got) != 0 {
		t.Fatalf("want no events for an [internal] vertex with N/N fraction, got %+v", got)
	}
}
