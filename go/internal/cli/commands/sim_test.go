package commands

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

func TestNewSimCmd_Subcommands(t *testing.T) {
	cmd := newSimCmd()
	if cmd.Use != "sim" {
		t.Errorf("Use = %q; want sim", cmd.Use)
	}
	want := map[string]bool{
		"create":         false,
		"list":           false,
		"import-model":   false,
		"describe-model": false,
		"spawn":          false,
		"state":          false,
		"reset":          false,
		"run":            false,
		"replay":         false,
	}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing subcommand %q", name)
		}
	}
}

func TestValidateImportModelSource(t *testing.T) {
	tests := []struct {
		name      string
		menagerie string
		local     string
		wantErr   bool
	}{
		{name: "menagerie only", menagerie: "unitree_go2/go2.xml", wantErr: false},
		{name: "local only", local: "./my-model", wantErr: false},
		{name: "neither", wantErr: true},
		{name: "both", menagerie: "unitree_go2/go2.xml", local: "./my-model", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateImportModelSource(tt.menagerie, tt.local)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateImportModelSource(%q, %q) = %v; wantErr %v",
					tt.menagerie, tt.local, err, tt.wantErr)
			}
		})
	}
}

func TestSimRunCmd_FlagDefaults(t *testing.T) {
	cmd := newSimRunCmd()

	if f := cmd.Flags().Lookup("record"); f == nil || f.DefValue != "true" {
		t.Errorf("--record default = %v; want true", f)
	}
	if f := cmd.Flags().Lookup("control-level"); f == nil || f.DefValue != "motion" {
		t.Errorf("--control-level default = %v; want motion", f)
	}
	if f := cmd.Flags().Lookup("output"); f == nil || f.Shorthand != "o" {
		t.Errorf("--output flag = %v; want shorthand -o", f)
	}
	for _, name := range []string{"world", "robot"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("missing --%s flag", name)
		}
		if req, ok := f.Annotations[cobra.BashCompOneRequiredFlag]; !ok || len(req) == 0 || req[0] != "true" {
			t.Errorf("--%s should be required", name)
		}
	}
}

func TestSimRunCmd_RequiresWorldAndRobot(t *testing.T) {
	cmd := newSimRunCmd()
	cmd.SetArgs([]string{"task.yaml"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Error("run without --world/--robot should fail")
	}
}

func TestTaskEventJSONLine(t *testing.T) {
	progress := &simpb.TaskEvent{Event: &simpb.TaskEvent_Progress{
		Progress: &simpb.TaskProgress{Objective: "move_forward", SimTimeS: 1.5},
	}}
	line, ok := taskEventJSONLine(progress)
	if !ok {
		t.Fatal("taskEventJSONLine(progress) ok = false; want true")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("progress line %q is not JSON: %v", line, err)
	}
	if got["event"] != "progress" || got["objective"] != "move_forward" || got["simTimeS"] != 1.5 {
		t.Errorf("progress line = %q; want event/objective/simTimeS fields", line)
	}

	logEv := &simpb.TaskEvent{Event: &simpb.TaskEvent_Log{Log: &simpb.TaskLog{Message: "spawned"}}}
	line, ok = taskEventJSONLine(logEv)
	if !ok {
		t.Fatal("taskEventJSONLine(log) ok = false; want true")
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("log line %q is not JSON: %v", line, err)
	}
	if got["event"] != "log" || got["message"] != "spawned" {
		t.Errorf("log line = %q; want event/message fields", line)
	}

	result := &simpb.TaskEvent{Event: &simpb.TaskEvent_Result{Result: &simpb.TaskResult{Success: true}}}
	if _, ok := taskEventJSONLine(result); ok {
		t.Error("taskEventJSONLine(result) ok = true; want false (rendered separately)")
	}
}

func TestTaskResultJSON(t *testing.T) {
	got := taskResultJSON(&simpb.TaskResult{
		Success:           false,
		Fell:              true,
		CollisionCount:    3,
		DistanceTraveledM: 1.25,
		Checks: []*simpb.CheckResult{
			{Name: "not_fallen", Passed: false, Detail: "fell at 2.1s"},
		},
		ReplayId: "r-42",
		Summary:  "robot fell",
	})
	if got.Success || !got.Fell || got.CollisionCount != 3 || got.DistanceTraveledM != 1.25 {
		t.Errorf("taskResultJSON scalars = %+v", got)
	}
	if got.ReplayID != "r-42" || got.Summary != "robot fell" {
		t.Errorf("taskResultJSON replay/summary = %+v", got)
	}
	if len(got.Checks) != 1 || got.Checks[0].Name != "not_fallen" || got.Checks[0].Passed ||
		got.Checks[0].Detail != "fell at 2.1s" {
		t.Errorf("taskResultJSON checks = %+v", got.Checks)
	}
}

func TestRenderTaskResult(t *testing.T) {
	passed := renderTaskResult(&simpb.TaskResult{
		Success:           true,
		DistanceTraveledM: 2.5,
		Checks: []*simpb.CheckResult{
			{Name: "not_fallen", Passed: true},
			{Name: "reached_goal", Passed: true},
		},
		Summary: "walked 2.5 m",
	})
	for _, want := range []string{"Task PASSED", "Fell:       no", "Distance:   2.50 m",
		"Collisions: 0", "✓ not_fallen", "✓ reached_goal", "Summary: walked 2.5 m"} {
		if !strings.Contains(passed, want) {
			t.Errorf("passed rendering missing %q:\n%s", want, passed)
		}
	}

	failed := renderTaskResult(&simpb.TaskResult{
		Success:        false,
		Fell:           true,
		CollisionCount: 2,
		Checks: []*simpb.CheckResult{
			{Name: "not_fallen", Passed: false, Detail: "fell at 2.1s"},
		},
	})
	for _, want := range []string{"Task FAILED", "Fell:       yes", "Collisions: 2",
		"✗ not_fallen — fell at 2.1s"} {
		if !strings.Contains(failed, want) {
			t.Errorf("failed rendering missing %q:\n%s", want, failed)
		}
	}
	if strings.Contains(failed, "Summary:") {
		t.Errorf("failed rendering should omit an empty summary:\n%s", failed)
	}
}

func TestSimImportModelCmd_RequiresName(t *testing.T) {
	cmd := newSimImportModelCmd()
	cmd.SetArgs([]string{"--menagerie", "unitree_go2/go2.xml"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Error("import-model without --name should fail")
	}
}
