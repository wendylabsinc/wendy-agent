package simutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

func TestParsePosition(t *testing.T) {
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
			got, err := ParsePosition(tt.pos)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePosition(%q) error = %v; wantErr %v", tt.pos, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("ParsePosition(%q) = %v; want %v", tt.pos, got, tt.want)
			}
			if got == nil {
				return
			}
			if got.GetX() != tt.want.GetX() || got.GetY() != tt.want.GetY() ||
				got.GetZ() != tt.want.GetZ() || got.GetQw() != tt.want.GetQw() {
				t.Errorf("ParsePosition(%q) = %+v; want %+v", tt.pos, got, tt.want)
			}
		})
	}
}

func TestParseVector3(t *testing.T) {
	tests := []struct {
		s       string
		want    [3]float64
		wantErr bool
	}{
		{s: "1,2,0.5", want: [3]float64{1, 2, 0.5}},
		{s: " -30 , 0 , 5 ", want: [3]float64{-30, 0, 5}},
		{s: "", wantErr: true},
		{s: "1,2", wantErr: true},
		{s: "1,2,3,4", wantErr: true},
		{s: "a,b,c", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			got, err := ParseVector3(tt.s, "force")
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseVector3(%q) error = %v; wantErr %v", tt.s, err, tt.wantErr)
			}
			if tt.wantErr {
				if err != nil && !strings.Contains(err.Error(), "force") {
					t.Errorf("error %q should name the value (force)", err.Error())
				}
				return
			}
			if got != tt.want {
				t.Errorf("ParseVector3(%q) = %v; want %v", tt.s, got, tt.want)
			}
		})
	}
}

func TestControlLevelName(t *testing.T) {
	if got := ControlLevelName(simpb.ControlLevel_CONTROL_LEVEL_MOTION); got != "motion" {
		t.Errorf("ControlLevelName(MOTION) = %q; want motion", got)
	}
	if got := ControlLevelName(simpb.ControlLevel_CONTROL_LEVEL_UNSPECIFIED); got != "unspecified" {
		t.Errorf("ControlLevelName(UNSPECIFIED) = %q; want unspecified", got)
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
			got, err := ParseControlLevel(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseControlLevel(%q) error = %v; wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseControlLevel(%q) = %v; want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSendDataChunks(t *testing.T) {
	// 2.5 chunks worth of data must arrive as 64KiB, 64KiB, 32KiB.
	payload := bytes.Repeat([]byte{0xAB}, ModelChunkSize*2+ModelChunkSize/2)

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
		if len(c) > ModelChunkSize {
			t.Errorf("chunk %d is %d bytes; want <= %d", i, len(c), ModelChunkSize)
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
	err := StreamLocalModel(dir, func(data []byte) error {
		if len(data) > ModelChunkSize {
			t.Errorf("chunk of %d bytes exceeds %d", len(data), ModelChunkSize)
		}
		streamed.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamLocalModel: %v", err)
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
	// Build a .tar.gz and verify StreamLocalModel transparently gunzips it so
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
	if err := StreamLocalModel(archive, func(data []byte) error {
		streamed.Write(data)
		return nil
	}); err != nil {
		t.Fatalf("StreamLocalModel: %v", err)
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
	if err := StreamLocalModel(archive, func(data []byte) error {
		streamed.Write(data)
		return nil
	}); err != nil {
		t.Fatalf("StreamLocalModel: %v", err)
	}
	if !bytes.Equal(streamed.Bytes(), tarBuf.Bytes()) {
		t.Error("streamed plain tar differs from the source file")
	}
}

func TestStreamLocalModel_Missing(t *testing.T) {
	err := StreamLocalModel(filepath.Join(t.TempDir(), "nope"), func([]byte) error { return nil })
	if err == nil {
		t.Error("StreamLocalModel on a missing path should fail")
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
	n, err := WriteReplayFile(path, func() ([]byte, error) {
		if i >= len(chunks) {
			return nil, io.EOF
		}
		c := chunks[i]
		i++
		return c, nil
	})
	if err != nil {
		t.Fatalf("WriteReplayFile: %v", err)
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
	_, err := WriteReplayFile(path, func() ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("partial"), nil
		}
		return nil, errors.New("stream broke")
	})
	if err == nil || !strings.Contains(err.Error(), "stream broke") {
		t.Fatalf("WriteReplayFile error = %v; want stream broke", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("partial file %s should have been removed (stat err = %v)", path, statErr)
	}
}

func TestResolveModelFormat(t *testing.T) {
	sdfDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sdfDir, "model.sdf"), []byte("<sdf/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	urdfDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(urdfDir, "robot.urdf"), []byte("<robot/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	mjcfDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(mjcfDir, "scene.xml"), []byte("<mujoco/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, explicit, menagerie, local string
		want                             simpb.ModelFormat
		wantErr                          bool
	}{
		{"explicit sdf wins", "sdf", "", mjcfDir, simpb.ModelFormat_MODEL_FORMAT_SDF, false},
		{"explicit urdf", "URDF", "", "", simpb.ModelFormat_MODEL_FORMAT_URDF, false},
		{"invalid explicit", "step", "", "", 0, true},
		{"menagerie is mjcf", "", "unitree_go2/go2.xml", "", simpb.ModelFormat_MODEL_FORMAT_MJCF, false},
		{"sdf dir detected", "", "", sdfDir, simpb.ModelFormat_MODEL_FORMAT_SDF, false},
		{"urdf dir detected", "", "", urdfDir, simpb.ModelFormat_MODEL_FORMAT_URDF, false},
		{"xml dir stays mjcf", "", "", mjcfDir, simpb.ModelFormat_MODEL_FORMAT_MJCF, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveModelFormat(tc.explicit, tc.menagerie, tc.local)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
