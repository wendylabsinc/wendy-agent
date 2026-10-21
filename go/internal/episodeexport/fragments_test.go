package episodeexport

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestJoinIndexedH264FragmentsPreservesSliceBytes(t *testing.T) {
	payloads := [][]byte{annexB([]byte{0x65, 0xB8, 0xAA}), {0xBB, 0xCC}, {0xDD, 0xEE}, annexB([]byte{0x41, 0xC0})}
	dir, src := writeSingleSourceEpisode(t, []int64{0, 100, 200, 1000}, payloads)
	frames, _, err := readCameraIndex(filepath.Join(src, "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	r := &segmentReader{root: dir, seen: map[string]bool{}}
	defer r.Close()
	joined, n, err := joinIndexedH264Fragments(r, frames)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(joined) != 2 {
		t.Fatalf("joined=%d fragments=%d", len(joined), n)
	}
	b, err := r.read(joined[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, bytes.Join(payloads[:3], nil)) {
		t.Fatalf("coded slice was truncated: %x", b)
	}
	if joined[0].CanonicalEpisodeNanos != 0 || joined[1].CanonicalEpisodeNanos != 1000 {
		t.Fatal("timestamps changed")
	}
}

func TestJoinIndexedH264FragmentsRefusesGaps(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "segment.h264"), []byte{0, 0, 0, 1, 0x65, 0xB8, 0xAA, 0xBB}, 0600); err != nil {
		t.Fatal(err)
	}
	r := &segmentReader{root: dir, seen: map[string]bool{}}
	defer r.Close()
	_, _, err := joinIndexedH264Fragments(r, []cameraIndexLine{{Segment: "segment.h264", ByteSize: 6}, {Segment: "segment.h264", ByteOffset: 7, ByteSize: 1}})
	if err == nil {
		t.Fatal("joined across missing bytes")
	}
}
