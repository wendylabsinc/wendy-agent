package data

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/episodeexport"
)

// The parameter sets are the ones a Jetson episode really carried, matching
// the episodeexport package's own fixtures, so the seal-time mux is exercised
// against the bitstream shape the device produces.
const (
	sealTestSPS = "6764001facb200a00b7602dc08081a94000003000400000300f23c60c920"
	sealTestPPS = "68ebccb22c"
)

// Slice NAL units whose headers the muxer can and cannot parse. The seal only
// keeps a clip whose timing it can vouch for, so the distinction is load
// bearing: an IDR or P slice with a parseable header is publishable, a B
// slice or an unparseable header is not.
var (
	parsedIDR     = append([]byte{0x65, 0x88}, make([]byte, 40)...) // slice_type 7 (I), parses
	parsedPSlice  = append([]byte{0x41, 0xC0}, make([]byte, 30)...) // slice_type 0 (P), parses
	parsedBSlice  = append([]byte{0x41, 0xA0}, make([]byte, 30)...) // slice_type 1 (B), parses
	unparsedSlice = append([]byte{0x41}, make([]byte, 30)...)       // header is all zero bits, cannot parse
)

func annexB(nals ...[]byte) []byte {
	var out []byte
	for _, n := range nals {
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	return out
}

// writeCameraSource lays one camera source into an active episode directory:
// a single segment holding the given access units at irregular intervals, and
// the index.jsonl that records where each frame's bytes landed.
func writeCameraSource(t *testing.T, episodeDir, source string, accessUnits [][]byte) {
	t.Helper()
	sps, err := hex.DecodeString(sealTestSPS)
	if err != nil {
		t.Fatal(err)
	}
	pps, err := hex.DecodeString(sealTestPPS)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(episodeDir, "cameras", source)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	segment := "cameras/" + source + "/segment-000001.h264"
	// Irregular gaps on purpose: the remux must carry recorded timestamps,
	// never a rate, and equal gaps could hide a muxer that assumed one.
	gaps := []int64{0, 33_000_000, 95_000_000, 41_000_000, 62_000_000, 27_000_000}
	var (
		seg   []byte
		index []byte
		now   = int64(2_000_000_000)
	)
	for i, unit := range accessUnits {
		payload := unit
		if i == 0 {
			payload = append(annexB(sps, pps), unit...)
		}
		now += gaps[i%len(gaps)]
		line, err := json.Marshal(map[string]any{
			"canonical_episode_nanos": now,
			"segment":                 segment,
			"byte_offset":             len(seg),
			"byte_size":               len(payload),
			"codec":                   "h264",
		})
		if err != nil {
			t.Fatal(err)
		}
		seg = append(seg, payload...)
		index = append(index, line...)
		index = append(index, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "segment-000001.h264"), seg, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.jsonl"), index, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileByPath(files []File, path string) (File, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return File{}, false
}

func TestSealWritesPlayableClipListedInManifest(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Start(StartOptions{Sources: []string{"applications"}}); err != nil {
		t.Fatal(err)
	}
	session, ok := m.ActiveSession(AdHocEpisodeKey)
	if !ok {
		t.Fatal("no active session")
	}
	writeCameraSource(t, session.Directory, "cam-front", [][]byte{
		annexB(parsedIDR), annexB(parsedPSlice), annexB(parsedPSlice),
	})
	stopped, err := m.Stop(AdHocEpisodeKey)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != "complete" {
		t.Fatalf("state %q, want complete", stopped.State)
	}
	if len(stopped.PlayableNotes) != 0 {
		t.Fatalf("clean mux left notes: %v", stopped.PlayableNotes)
	}

	rel := "cameras/cam-front/" + episodeexport.PlayableFileName
	entry, ok := fileByPath(stopped.Files, rel)
	if !ok {
		t.Fatalf("manifest does not list %s: %+v", rel, stopped.Files)
	}
	if entry.Role != FileRoleDerived {
		t.Errorf("derived clip role %q, want %q", entry.Role, FileRoleDerived)
	}
	if entry.Format != "mp4" || entry.MediaType != "video/mp4" {
		t.Errorf("derived clip format %q media type %q, want mp4/video/mp4", entry.Format, entry.MediaType)
	}
	if entry.SourceID != "cam-front" {
		t.Errorf("derived clip source %q, want cam-front", entry.SourceID)
	}

	// The listed size and SHA-256 must be the finished file's, so the
	// transfer worker uploads it and commit-time verification covers it with
	// no special casing.
	onDisk := filepath.Join(m.root, stopped.ID, rel)
	hash, size, err := checksum(onDisk)
	if err != nil {
		t.Fatalf("derived clip missing from sealed episode: %v", err)
	}
	if entry.SHA256 != hash || entry.Size != size {
		t.Errorf("manifest entry (%d bytes, %s) does not match file (%d bytes, %s)", entry.Size, entry.SHA256, size, hash)
	}
	if size == 0 {
		t.Error("derived clip is empty")
	}

	// The raw capture must still be listed, unmarked: only the remux is
	// derived, and payload accounting keys on the raw files via the index.
	for _, raw := range []string{"cameras/cam-front/segment-000001.h264", "cameras/cam-front/index.jsonl"} {
		f, ok := fileByPath(stopped.Files, raw)
		if !ok {
			t.Fatalf("raw capture file %s missing from manifest", raw)
		}
		if f.Role != "" {
			t.Errorf("raw capture file %s carries role %q, want none", raw, f.Role)
		}
	}

	// Full verification passes with the derived file treated like any other
	// listed file.
	if _, failures, err := m.Inspect(stopped.ID, true); err != nil || len(failures) != 0 {
		t.Fatalf("verification: err=%v failures=%v", err, failures)
	}

	// The derived clip counts against the episode's size, and so against the
	// quota that size feeds.
	infos, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, f := range stopped.Files {
		total += f.Size
	}
	if len(infos) != 1 || infos[0].SizeBytes != total {
		t.Fatalf("episode size %d does not include every listed file (want %d)", infos[0].SizeBytes, total)
	}
	var withoutDerived int64
	for _, f := range stopped.Files {
		if f.Role != FileRoleDerived {
			withoutDerived += f.Size
		}
	}
	if withoutDerived >= total {
		t.Error("derived clip contributed nothing to the episode size")
	}
}

func TestSealRefusesClipItCannotVouchFor(t *testing.T) {
	cases := []struct {
		name       string
		accessUnit []byte
		wantNote   string
	}{
		{"b_slices", annexB(parsedBSlice), "B slices"},
		{"unparsed_slice_header", annexB(unparsedSlice), "slice header"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = m.Start(StartOptions{Sources: []string{"applications"}}); err != nil {
				t.Fatal(err)
			}
			session, ok := m.ActiveSession(AdHocEpisodeKey)
			if !ok {
				t.Fatal("no active session")
			}
			writeCameraSource(t, session.Directory, "cam-front", [][]byte{
				annexB(parsedIDR), tc.accessUnit,
			})
			stopped, err := m.Stop(AdHocEpisodeKey)
			if err != nil {
				t.Fatalf("a refused mux must never fail the seal: %v", err)
			}
			if stopped.State != "complete" {
				t.Fatalf("state %q, want complete", stopped.State)
			}
			rel := "cameras/cam-front/" + episodeexport.PlayableFileName
			if _, ok := fileByPath(stopped.Files, rel); ok {
				t.Errorf("manifest lists %s despite the refusal", rel)
			}
			if _, err := os.Stat(filepath.Join(m.root, stopped.ID, rel)); !os.IsNotExist(err) {
				t.Errorf("refused clip left on disk (stat err %v)", err)
			}
			if len(stopped.PlayableNotes) != 1 || !strings.Contains(stopped.PlayableNotes[0], tc.wantNote) {
				t.Errorf("notes %v do not name the refusal (%q)", stopped.PlayableNotes, tc.wantNote)
			}
			if !strings.Contains(stopped.PlayableNotes[0], rel+" not written") {
				t.Errorf("note %q does not name the missing file", stopped.PlayableNotes[0])
			}
			if _, failures, err := m.Inspect(stopped.ID, true); err != nil || len(failures) != 0 {
				t.Fatalf("verification: err=%v failures=%v", err, failures)
			}
		})
	}
}

func TestSealRefusesClipWithoutRandomAccessFrame(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Start(StartOptions{Sources: []string{"applications"}}); err != nil {
		t.Fatal(err)
	}
	session, ok := m.ActiveSession(AdHocEpisodeKey)
	if !ok {
		t.Fatal("no active session")
	}
	// Parameter sets but never an IDR: nothing a decoder can start on.
	writeCameraSource(t, session.Directory, "cam-front", [][]byte{
		annexB(parsedPSlice), annexB(parsedPSlice),
	})
	stopped, err := m.Stop(AdHocEpisodeKey)
	if err != nil {
		t.Fatal(err)
	}
	rel := "cameras/cam-front/" + episodeexport.PlayableFileName
	if _, ok := fileByPath(stopped.Files, rel); ok {
		t.Errorf("manifest lists %s despite it having no sync sample", rel)
	}
	if len(stopped.PlayableNotes) != 1 || !strings.Contains(stopped.PlayableNotes[0], "random-access") {
		t.Errorf("notes %v do not name the missing random-access frame", stopped.PlayableNotes)
	}
}

func TestRecoverySealsPlayableClipToo(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	started, err := m.Start(StartOptions{Sources: []string{"applications"}})
	if err != nil {
		t.Fatal(err)
	}
	writeCameraSource(t, filepath.Join(root, started.ID+".partial"), "cam-front", [][]byte{
		annexB(parsedIDR), annexB(parsedPSlice),
	})
	// A new manager over the same root recovers the abandoned partial.
	m2, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	mf, failures, err := m2.Inspect(started.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if mf.State != "interrupted" {
		t.Fatalf("state %q, want interrupted", mf.State)
	}
	if len(failures) != 0 {
		t.Fatalf("verification failures: %v", failures)
	}
	rel := "cameras/cam-front/" + episodeexport.PlayableFileName
	entry, ok := fileByPath(mf.Files, rel)
	if !ok {
		t.Fatalf("recovered manifest does not list %s", rel)
	}
	if entry.Role != FileRoleDerived {
		t.Errorf("recovered clip role %q, want %q", entry.Role, FileRoleDerived)
	}
	if len(mf.PlayableNotes) != 0 {
		t.Errorf("clean recovery mux left notes: %v", mf.PlayableNotes)
	}
}

func TestIsDerivedPlayable(t *testing.T) {
	for path, want := range map[string]bool{
		"cameras/cam-front/playable.mp4":        true,
		"cameras/cam-front/segment-000001.h264": false,
		"playable.mp4":                          false,
		"cameras/playable.mp4":                  false,
		"audio/mic/playable.mp4":                false,
		"cameras/cam/deeper/playable.mp4":       false,
	} {
		if got := isDerivedPlayable(path); got != want {
			t.Errorf("isDerivedPlayable(%q) = %v, want %v", path, got, want)
		}
	}
}
