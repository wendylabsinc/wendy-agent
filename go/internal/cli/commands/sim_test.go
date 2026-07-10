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
		"pause":          false,
		"resume":         false,
		"speed":          false,
		"push":           false,
		"teleport":       false,
		"snapshot":       false,
		"sensors":        false,
		"scene":          false,
		"policy":         false,
		"record":         false,
		"joints":         false,
		"step":           false,
		"eval":           false,
		"teleop":         false,
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

func TestRenderWatchFrame(t *testing.T) {
	resp := &simpb.GetStateResponse{
		BasePose: &simpb.Pose{X: 1.5, Y: 0, Z: 0.25},
		SimTimeS: 12.34,
		Fallen:   true,
		Joints:   []*simpb.JointState{{Name: "FL_hip_joint", Position: 0.1, Velocity: -0.2}},
	}
	out := renderWatchFrame("world1", "robot3", resp)
	for _, want := range []string{"world1", "robot3", "FALLEN", "FL_hip_joint", "12.34"} {
		if !strings.Contains(out, want) {
			t.Errorf("watch frame missing %q:\n%s", want, out)
		}
	}
}

func TestWatchJSONLine(t *testing.T) {
	line := watchJSONLine(&simpb.GetStateResponse{
		BasePose: &simpb.Pose{X: 2, Z: 0.3}, SimTimeS: 5, Fallen: false,
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got["x"] != 2.0 || got["fallen"] != false {
		t.Errorf("unexpected fields: %v", got)
	}
}

func TestSimDriveCmd_Flags(t *testing.T) {
	cmd := newSimDriveCmd()
	for _, f := range []string{"world", "robot", "vx", "vy", "yaw", "stop"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("drive missing flag --%s", f)
		}
	}
}

func TestSimInteractiveCmds_Flags(t *testing.T) {
	tests := []struct {
		name  string
		cmd   *cobra.Command
		flags []string
	}{
		{name: "pause", cmd: newSimPauseCmd(), flags: []string{"world"}},
		{name: "resume", cmd: newSimResumeCmd(), flags: []string{"world"}},
		{name: "speed", cmd: newSimSpeedCmd(), flags: []string{"world"}},
		{name: "push", cmd: newSimPushCmd(), flags: []string{"world", "robot", "force", "duration"}},
		{name: "teleport", cmd: newSimTeleportCmd(), flags: []string{"world", "robot", "pos"}},
		{name: "snapshot save", cmd: newSimSnapshotSaveCmd(), flags: []string{"world"}},
		{name: "snapshot restore", cmd: newSimSnapshotRestoreCmd(), flags: []string{"world"}},
		{name: "sensors", cmd: newSimSensorsCmd(), flags: []string{"world", "robot"}},
		{name: "scene add-box", cmd: newSimSceneAddBoxCmd(), flags: []string{"world", "id", "pos", "size"}},
		{name: "scene remove", cmd: newSimSceneRemoveCmd(), flags: []string{"world", "id"}},
		{name: "policy load", cmd: newSimPolicyLoadCmd(), flags: []string{"world", "robot", "format"}},
		{name: "policy clear", cmd: newSimPolicyClearCmd(), flags: []string{"world", "robot"}},
		{name: "record", cmd: newSimRecordCmd(), flags: []string{"world", "robot", "duration", "fps", "output", "camera", "width", "height"}},
		{name: "joints get", cmd: newSimJointsGetCmd(), flags: []string{"world", "robot"}},
		{name: "joints set", cmd: newSimJointsSetCmd(), flags: []string{"world", "robot"}},
		{name: "step", cmd: newSimStepCmd(), flags: []string{"world", "seconds"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, f := range tt.flags {
				if tt.cmd.Flags().Lookup(f) == nil {
					t.Errorf("%s missing flag --%s", tt.name, f)
				}
			}
		})
	}
}

func TestSimSnapshotAndSceneCmd_Subcommands(t *testing.T) {
	for _, tt := range []struct {
		cmd  *cobra.Command
		want []string
	}{
		{cmd: newSimSnapshotCmd(), want: []string{"save", "restore"}},
		{cmd: newSimSceneCmd(), want: []string{"add-box", "remove"}},
		{cmd: newSimPolicyCmd(), want: []string{"load", "clear"}},
		{cmd: newSimJointsCmd(), want: []string{"get", "set"}},
	} {
		found := map[string]bool{}
		for _, sub := range tt.cmd.Commands() {
			found[sub.Name()] = true
		}
		for _, name := range tt.want {
			if !found[name] {
				t.Errorf("%s missing subcommand %q", tt.cmd.Name(), name)
			}
		}
	}
}

func TestSimImportModelCmd_HasReplaceFlag(t *testing.T) {
	cmd := newSimImportModelCmd()
	if cmd.Flags().Lookup("replace") == nil {
		t.Error("import-model missing flag --replace")
	}
}

func TestSimSpeedCmd_RejectsBadFactor(t *testing.T) {
	for _, arg := range []string{"abc", "0", "-2"} {
		cmd := newSimSpeedCmd()
		cmd.SetArgs([]string{arg, "--world", "w1"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Errorf("speed %q should fail", arg)
		}
	}
}

func TestParseJointTargets(t *testing.T) {
	got, err := parseJointTargets([]string{"FL_hip=0.5", "FR_hip=-0.25"})
	if err != nil {
		t.Fatalf("parseJointTargets: %v", err)
	}
	if len(got) != 2 || got["FL_hip"] != 0.5 || got["FR_hip"] != -0.25 {
		t.Errorf("targets = %v", got)
	}

	for _, bad := range []string{"FL_hip", "FL_hip=abc", "=0.5"} {
		if _, err := parseJointTargets([]string{bad}); err == nil {
			t.Errorf("parseJointTargets(%q) should fail", bad)
		}
	}
}
