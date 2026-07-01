package tui

import (
	"strings"
	"testing"
	"time"
)

func TestPlainRendererWritesLinePerCompletedStep(t *testing.T) {
	var sb strings.Builder
	emit, tally := NewBuildPlainRenderer(&sb)
	emit(BuildStepEvent{ID: 1, Kind: BuildVertexSetup, Display: "load metadata", Status: BuildStepRunning})
	emit(BuildStepEvent{ID: 1, Kind: BuildVertexSetup, Display: "load metadata", Status: BuildStepDone, Dur: 2 * time.Second})
	emit(BuildStepEvent{ID: 6, Kind: BuildVertexStep, Display: "[1/6] FROM python", Status: BuildStepRunning})
	emit(BuildStepEvent{ID: 6, Kind: BuildVertexStep, Display: "[1/6] FROM python", Status: BuildStepCached})
	emit(BuildStepEvent{ID: 9, Kind: BuildVertexStep, Display: "[4/6] RUN pip install", Status: BuildStepRunning})
	emit(BuildStepEvent{ID: 9, Kind: BuildVertexStep, Display: "[4/6] RUN pip install", Status: BuildStepDone, Dur: 4300 * time.Millisecond})

	out := sb.String()
	// Running events produce no line; only terminal states do.
	for _, want := range []string{
		"load metadata", "2.0s",
		"[1/6] FROM python", "cached",
		"[4/6] RUN pip install", "4.3s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plain output missing %q\n%s", want, out)
		}
	}
	if got := tally(); got.Cached != 1 || got.Rebuilt != 1 {
		t.Errorf("tally = %+v, want {Cached:1 Rebuilt:1}", got)
	}
}
