package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestCameraDeviceID(t *testing.T) {
	for input, want := range map[string]uint32{"v4l2:/dev/video2": 2, "ipcamera:203": 203} {
		got, ok := cameraDeviceID(input)
		if !ok || got != want {
			t.Fatalf("cameraDeviceID(%q) = %d, %v; want %d, true", input, got, ok, want)
		}
	}
	if _, ok := cameraDeviceID("v4l2:/tmp/video0"); ok {
		t.Fatal("accepted unsafe V4L2 source id")
	}
}

func TestV4L2BufferTimestampMetadata(t *testing.T) {
	var buffer v4l2Buf
	binary.LittleEndian.PutUint32(buffer[12:16], 0x2000)
	binary.LittleEndian.PutUint64(buffer[24:32], 12)
	binary.LittleEndian.PutUint64(buffer[32:40], 3456)
	binary.LittleEndian.PutUint32(buffer[56:60], 91)
	if got := buffer.timestampNanos(); got != 12*int64(time.Second)+3456*int64(time.Microsecond) {
		t.Fatalf("timestamp = %d", got)
	}
	if buffer.flags() != 0x2000 || buffer.sequence() != 91 {
		t.Fatalf("metadata flags=%x sequence=%d", buffer.flags(), buffer.sequence())
	}
}

func TestCameraCaptureWritesSegmentAndAuditableIndex(t *testing.T) {
	dir := t.TempDir()
	index, err := os.OpenFile(filepath.Join(dir, "index.jsonl"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	mappings, err := os.OpenFile(filepath.Join(dir, "clock_samples.jsonl"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, origin, _, err := data.CaptureReceipt()
	if err != nil {
		t.Fatal(err)
	}
	capture := &cameraCapture{source: data.Source{ID: "v4l2:/dev/video2"}, session: data.CaptureSession{Directory: filepath.Dir(filepath.Dir(dir)), RequestBootNanos: origin}, dir: dir, index: index, mappingFile: mappings}
	frame := &videoFrame{data: []byte{0, 0, 0, 1, 0x67, 1, 0, 0, 0, 1, 0x68, 2, 0, 0, 0, 1, 0x65, 3}, tsNs: uint64(time.Now().UnixNano()), nativeNs: time.Now().UnixNano(), nativeClock: "CLOCK_REALTIME_AGENT_CAPTURE", codec: agentpb.VideoCodec_VIDEO_CODEC_H264}
	if err := capture.writeFrame(frame); err != nil {
		t.Fatal(err)
	}
	if err := capture.segment.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := index.Sync(); err != nil {
		t.Fatal(err)
	}
	media, err := os.ReadFile(filepath.Join(dir, "segment-000001.h264"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(media, frame.data) {
		t.Fatalf("media = %x", media)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var record cameraIndexRecord
	if err := json.Unmarshal(bytes.TrimSpace(contents), &record); err != nil {
		t.Fatal(err)
	}
	if record.CanonicalEpisodeNanos < 0 || record.CanonicalUncertaintyNanos < 0 || record.AgentReceiptBootNanos == 0 {
		t.Fatalf("bad canonical record: %+v", record)
	}
	if record.MappingSegment != "receipt-bracket-v1" || record.ByteSize != len(frame.data) {
		t.Fatalf("bad index metadata: %+v", record)
	}
	_ = capture.segment.Close()
	_ = index.Close()
	_ = mappings.Close()
}

func TestH264SegmentsWaitForSelfContainedRandomAccessUnit(t *testing.T) {
	inter := []byte{0, 0, 0, 1, 0x41, 1}
	key := []byte{9, 9, 9, 0, 0, 1, 0x67, 1, 0, 0, 1, 0x68, 2, 0, 0, 1, 0x65, 3}
	if _, found := h264RandomAccessOffset(inter); found {
		t.Fatal("inter frame reported as random access")
	}
	if offset, found := h264RandomAccessOffset(key); !found || offset != 3 {
		t.Fatal("SPS/PPS/IDR access unit not recognized")
	}
}

func TestDeviceHubCountsSubscriberDrops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	hub := &deviceHub{subs: make(map[int]chan *videoFrame), subDrops: make(map[int]uint64), ctx: ctx, cancel: cancel}
	id, _, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		hub.broadcast(&videoFrame{data: []byte{byte(i)}})
	}
	if got := hub.unsubscribe(id); got != 2 {
		t.Fatalf("drops = %d, want 2", got)
	}
}
