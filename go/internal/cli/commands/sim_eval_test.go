package commands

import (
	"io"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

func TestSummarizeEval(t *testing.T) {
	results := []*simpb.TaskResult{
		{Success: true, DistanceTraveledM: 3.0},
		{Success: false, Fell: true, DistanceTraveledM: 1.0},
		{Success: true, DistanceTraveledM: 2.0},
		{Success: false, Fell: true, DistanceTraveledM: 0.5},
	}
	s := summarizeEval(results)
	if s.Episodes != 4 || s.Successes != 2 || s.Falls != 2 {
		t.Errorf("episodes/successes/falls = %d/%d/%d; want 4/2/2", s.Episodes, s.Successes, s.Falls)
	}
	if s.SuccessRate != 0.5 {
		t.Errorf("success rate = %v; want 0.5", s.SuccessRate)
	}
	if s.MeanDistM != 1.625 || s.MinDistM != 0.5 || s.MaxDistM != 3.0 {
		t.Errorf("distance mean/min/max = %v/%v/%v; want 1.625/0.5/3", s.MeanDistM, s.MinDistM, s.MaxDistM)
	}
}

func TestSummarizeEval_Empty(t *testing.T) {
	s := summarizeEval(nil)
	if s.Episodes != 0 || s.Successes != 0 || s.SuccessRate != 0 {
		t.Errorf("empty summary = %+v; want zeros", s)
	}
}

func TestRenderEvalSummary(t *testing.T) {
	out := renderEvalSummary(evalSummary{
		Episodes: 4, Successes: 2, SuccessRate: 0.5,
		Falls: 1, MeanDistM: 1.62, MinDistM: 0.5, MaxDistM: 3,
	})
	for _, want := range []string{
		"4 episode(s)", "50% (2/4)", "mean 1.62 m (min 0.50, max 3.00)", "Falls:        1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestSimEvalCmd_Flags(t *testing.T) {
	cmd := newSimEvalCmd()
	for _, f := range []string{"world", "robot", "episodes", "control-level"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("eval missing flag --%s", f)
		}
	}
	if f := cmd.Flags().Lookup("episodes"); f != nil && f.DefValue != "5" {
		t.Errorf("--episodes default = %s; want 5", f.DefValue)
	}
}

func TestSimEvalCmd_RequiresWorldAndRobot(t *testing.T) {
	cmd := newSimEvalCmd()
	cmd.SetArgs([]string{"task.yaml"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Error("eval without --world/--robot should fail")
	}
}
