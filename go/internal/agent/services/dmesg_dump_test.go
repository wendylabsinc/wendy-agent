package services

import (
	"errors"
	"strings"
	"syscall"
	"testing"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestStreamKmsgSnapshotParsesAndBatches(t *testing.T) {
	// Five valid records plus noise lines that must be skipped:
	//   - empty line
	//   - continuation line (leading space)
	//   - malformed line (no semicolon)
	input := strings.Join([]string{
		"6,1,100,-;first",
		"",
		"3,2,200,-;second",
		" continuation text that must be skipped",
		"4,3,300,-;third",
		"not a kmsg line",
		"0,4,400,-;fourth",
		"7,5,500,-;fifth",
	}, "\n")

	var batches [][]*agentpb.KernelLogRecord
	err := streamKmsgSnapshot(strings.NewReader(input), 2, func(recs []*agentpb.KernelLogRecord) error {
		// Copy the slice: production callers must not retain the buffer, and
		// the helper is free to reuse it across batches.
		batch := append([]*agentpb.KernelLogRecord(nil), recs...)
		batches = append(batches, batch)
		return nil
	})
	if err != nil {
		t.Fatalf("streamKmsgSnapshot returned error: %v", err)
	}

	// batchSize 2 over 5 valid records → [2, 2, 1].
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d: %v", len(batches), batches)
	}
	if len(batches[0]) != 2 || len(batches[1]) != 2 || len(batches[2]) != 1 {
		t.Fatalf("expected batch sizes [2 2 1], got [%d %d %d]",
			len(batches[0]), len(batches[1]), len(batches[2]))
	}

	var all []*agentpb.KernelLogRecord
	for _, b := range batches {
		all = append(all, b...)
	}
	want := []struct {
		ts    int64
		level int32
		msg   string
	}{
		{100, 6, "first"},
		{200, 3, "second"},
		{300, 4, "third"},
		{400, 0, "fourth"},
		{500, 7, "fifth"},
	}
	if len(all) != len(want) {
		t.Fatalf("expected %d records, got %d", len(want), len(all))
	}
	for i, w := range want {
		got := all[i]
		if got.GetTimestampUs() != w.ts || got.GetLevel() != w.level || got.GetMessage() != w.msg {
			t.Errorf("record %d = {ts:%d level:%d msg:%q}, want {ts:%d level:%d msg:%q}",
				i, got.GetTimestampUs(), got.GetLevel(), got.GetMessage(), w.ts, w.level, w.msg)
		}
	}
}

func TestStreamKmsgSnapshotPropagatesEmitError(t *testing.T) {
	input := "6,1,100,-;first\n6,2,200,-;second\n"
	sentinel := errors.New("send failed")
	calls := 0
	err := streamKmsgSnapshot(strings.NewReader(input), 1, func([]*agentpb.KernelLogRecord) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	// Must stop at the first failed emit, not keep sending.
	if calls != 1 {
		t.Fatalf("expected emit to be called once before aborting, got %d", calls)
	}
}

// eagainReader emits its records once, then mimics a non-blocking /dev/kmsg fd
// drained to end-of-buffer: unix.Read returns (-1, EAGAIN). The -1 violates the
// io.Reader contract and, before the kmsgSnapshotReader clamp, made
// bufio.Scanner abort with ErrBadReadCount ("Read returned impossible count")
// instead of surfacing the EAGAIN that signals a clean end-of-snapshot.
type eagainReader struct {
	data []byte
	done bool
}

func (r *eagainReader) Read(p []byte) (int, error) {
	if !r.done {
		n := copy(p, r.data)
		r.done = true
		return n, nil
	}
	// Match unix.Read's contract on error: count is -1, not 0.
	return -1, syscall.EAGAIN
}

func TestStreamKmsgSnapshotSurfacesEAGAINNotBadReadCount(t *testing.T) {
	r := &eagainReader{data: []byte("6,1,100,-;first\n3,2,200,-;second\n")}
	var got []*agentpb.KernelLogRecord
	err := streamKmsgSnapshot(r, 8, func(recs []*agentpb.KernelLogRecord) error {
		got = append(got, append([]*agentpb.KernelLogRecord(nil), recs...)...)
		return nil
	})
	if !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("expected EAGAIN to surface, got %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "impossible count") {
		t.Fatalf("scanner aborted with ErrBadReadCount instead of EAGAIN: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records parsed before EAGAIN, got %d", len(got))
	}
}

func TestStreamKmsgSnapshotEmptyInput(t *testing.T) {
	calls := 0
	err := streamKmsgSnapshot(strings.NewReader(""), 4, func([]*agentpb.KernelLogRecord) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no emit for empty input, got %d calls", calls)
	}
}
