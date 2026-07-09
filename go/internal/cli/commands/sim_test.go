package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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

func TestParseSimPosition(t *testing.T) {
	tests := []struct {
		pos     string
		want    *simpb.Pose
		wantErr bool
	}{
		{pos: "", want: nil},
		{pos: "1,2,0.5", want: &simpb.Pose{X: 1, Y: 2, Z: 0.5, Qw: 1}},
		{pos: " -1.5 , 0 , 3 ", want: &simpb.Pose{X: -1.5, Y: 0, Z: 3, Qw: 1}},
		{pos: "1,2", wantErr: true},
		{pos: "1,2,3,4", wantErr: true},
		{pos: "a,b,c", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.pos, func(t *testing.T) {
			got, err := parseSimPosition(tt.pos)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSimPosition(%q) error = %v; wantErr %v", tt.pos, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("parseSimPosition(%q) = %v; want %v", tt.pos, got, tt.want)
			}
			if got == nil {
				return
			}
			if got.GetX() != tt.want.GetX() || got.GetY() != tt.want.GetY() ||
				got.GetZ() != tt.want.GetZ() || got.GetQw() != tt.want.GetQw() {
				t.Errorf("parseSimPosition(%q) = %+v; want %+v", tt.pos, got, tt.want)
			}
		})
	}
}

func TestControlLevelName(t *testing.T) {
	if got := controlLevelName(simpb.ControlLevel_CONTROL_LEVEL_MOTION); got != "motion" {
		t.Errorf("controlLevelName(MOTION) = %q; want motion", got)
	}
	if got := controlLevelName(simpb.ControlLevel_CONTROL_LEVEL_UNSPECIFIED); got != "unspecified" {
		t.Errorf("controlLevelName(UNSPECIFIED) = %q; want unspecified", got)
	}
}

func TestSendDataChunks(t *testing.T) {
	// 2.5 chunks worth of data must arrive as 64KiB, 64KiB, 32KiB.
	payload := bytes.Repeat([]byte{0xAB}, simModelChunkSize*2+simModelChunkSize/2)

	var chunks [][]byte
	err := sendDataChunks(bytes.NewReader(payload), func(data []byte) error {
		chunks = append(chunks, data)
		return nil
	})
	if err != nil {
		t.Fatalf("sendDataChunks: %v", err)
	}

	var total []byte
	for i, c := range chunks {
		if len(c) > simModelChunkSize {
			t.Errorf("chunk %d is %d bytes; want <= %d", i, len(c), simModelChunkSize)
		}
		total = append(total, c...)
	}
	if !bytes.Equal(total, payload) {
		t.Errorf("reassembled payload differs: got %d bytes, want %d", len(total), len(payload))
	}
	if len(chunks) != 3 {
		t.Errorf("len(chunks) = %d; want 3", len(chunks))
	}
}

func TestSendDataChunks_Empty(t *testing.T) {
	calls := 0
	err := sendDataChunks(bytes.NewReader(nil), func([]byte) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("sendDataChunks: %v", err)
	}
	if calls != 0 {
		t.Errorf("send called %d times for empty input; want 0", calls)
	}
}

// writeModelDir builds a small MJCF-shaped model directory.
func writeModelDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"robot.xml":        "<mujoco/>",
		"assets/mesh.obj":  "v 0 0 0",
		"assets/notes.txt": "hello",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// readTarEntries maps entry name -> content for regular files in a tar stream.
func readTarEntries(t *testing.T, r io.Reader) map[string]string {
	t.Helper()
	entries := map[string]string{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading tar: %v", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			entries[hdr.Name] = ""
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading tar entry %s: %v", hdr.Name, err)
		}
		entries[hdr.Name] = string(data)
	}
	return entries
}

func TestTarDirectory(t *testing.T) {
	dir := writeModelDir(t)

	var buf bytes.Buffer
	if err := tarDirectory(dir, &buf); err != nil {
		t.Fatalf("tarDirectory: %v", err)
	}

	entries := readTarEntries(t, &buf)
	if entries["robot.xml"] != "<mujoco/>" {
		t.Errorf("robot.xml = %q; want <mujoco/>", entries["robot.xml"])
	}
	if entries["assets/mesh.obj"] != "v 0 0 0" {
		t.Errorf("assets/mesh.obj = %q; want v 0 0 0", entries["assets/mesh.obj"])
	}
	if _, ok := entries["assets/"]; !ok {
		t.Errorf("missing directory entry assets/; entries: %v", entries)
	}
}

func TestStreamLocalModel_Directory(t *testing.T) {
	dir := writeModelDir(t)

	var streamed bytes.Buffer
	err := streamLocalModel(dir, func(data []byte) error {
		if len(data) > simModelChunkSize {
			t.Errorf("chunk of %d bytes exceeds %d", len(data), simModelChunkSize)
		}
		streamed.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("streamLocalModel: %v", err)
	}

	entries := readTarEntries(t, &streamed)
	if entries["robot.xml"] != "<mujoco/>" {
		t.Errorf("streamed tar robot.xml = %q; want <mujoco/>", entries["robot.xml"])
	}
	if entries["assets/notes.txt"] != "hello" {
		t.Errorf("streamed tar assets/notes.txt = %q; want hello", entries["assets/notes.txt"])
	}
}

func TestStreamLocalModel_GzippedTar(t *testing.T) {
	// Build a .tar.gz and verify streamLocalModel transparently gunzips it so
	// the wire carries a plain tar.
	dir := writeModelDir(t)
	var tarBuf bytes.Buffer
	if err := tarDirectory(dir, &tarBuf); err != nil {
		t.Fatalf("tarDirectory: %v", err)
	}
	plainTar := tarBuf.Bytes()

	archive := filepath.Join(t.TempDir(), "model.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write(plainTar); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var streamed bytes.Buffer
	if err := streamLocalModel(archive, func(data []byte) error {
		streamed.Write(data)
		return nil
	}); err != nil {
		t.Fatalf("streamLocalModel: %v", err)
	}
	if !bytes.Equal(streamed.Bytes(), plainTar) {
		t.Errorf("streamed bytes differ from the plain tar (%d vs %d bytes)",
			streamed.Len(), len(plainTar))
	}
}

func TestStreamLocalModel_PlainTarFile(t *testing.T) {
	dir := writeModelDir(t)
	var tarBuf bytes.Buffer
	if err := tarDirectory(dir, &tarBuf); err != nil {
		t.Fatalf("tarDirectory: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "model.tar")
	if err := os.WriteFile(archive, tarBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	var streamed bytes.Buffer
	if err := streamLocalModel(archive, func(data []byte) error {
		streamed.Write(data)
		return nil
	}); err != nil {
		t.Fatalf("streamLocalModel: %v", err)
	}
	if !bytes.Equal(streamed.Bytes(), tarBuf.Bytes()) {
		t.Error("streamed plain tar differs from the source file")
	}
}

func TestStreamLocalModel_Missing(t *testing.T) {
	err := streamLocalModel(filepath.Join(t.TempDir(), "nope"), func([]byte) error { return nil })
	if err == nil {
		t.Error("streamLocalModel on a missing path should fail")
	}
}

func TestParseControlLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    simpb.ControlLevel
		wantErr bool
	}{
		{in: "task", want: simpb.ControlLevel_CONTROL_LEVEL_TASK},
		{in: "motion", want: simpb.ControlLevel_CONTROL_LEVEL_MOTION},
		{in: "joint", want: simpb.ControlLevel_CONTROL_LEVEL_JOINT},
		{in: "physics", want: simpb.ControlLevel_CONTROL_LEVEL_PHYSICS},
		{in: "MOTION", want: simpb.ControlLevel_CONTROL_LEVEL_MOTION},
		{in: " task ", want: simpb.ControlLevel_CONTROL_LEVEL_TASK},
		{in: "", wantErr: true},
		{in: "torque", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseControlLevel(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseControlLevel(%q) error = %v; wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseControlLevel(%q) = %v; want %v", tt.in, got, tt.want)
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

func TestWriteReplayFile(t *testing.T) {
	chunks := [][]byte{
		[]byte("rrd|"),
		bytes.Repeat([]byte{0x7F}, 1024),
		[]byte("|end"),
	}
	path := filepath.Join(t.TempDir(), "replay.rrd")

	i := 0
	n, err := writeReplayFile(path, func() ([]byte, error) {
		if i >= len(chunks) {
			return nil, io.EOF
		}
		c := chunks[i]
		i++
		return c, nil
	})
	if err != nil {
		t.Fatalf("writeReplayFile: %v", err)
	}

	want := bytes.Join(chunks, nil)
	if n != int64(len(want)) {
		t.Errorf("bytes written = %d; want %d", n, len(want))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("file content differs: got %d bytes, want %d", len(got), len(want))
	}
}

func TestWriteReplayFile_ErrorRemovesPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.rrd")
	calls := 0
	_, err := writeReplayFile(path, func() ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("partial"), nil
		}
		return nil, errors.New("stream broke")
	})
	if err == nil || !strings.Contains(err.Error(), "stream broke") {
		t.Fatalf("writeReplayFile error = %v; want stream broke", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("partial file %s should have been removed (stat err = %v)", path, statErr)
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
