package services

import (
	"context"
	"encoding/binary"
	"io"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/foxglovebridge"
)

// --- test helpers mirroring the foxglovebridge codec's little-endian layout ---

// appendU32 appends a little-endian uint32.
func appendU32(dst []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(dst, v) }

// appendU64 appends a little-endian uint64.
func appendU64(dst []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(dst, v) }

// readU32 reads a little-endian uint32 from the start of b.
func readU32(b []byte) uint32 { return binary.LittleEndian.Uint32(b[0:4]) }

// writeFrame writes a [u32 len][u8 tag][body] envelope to w, matching the
// codec's frame layout (len counts tag+body).
func writeFrame(w io.Writer, tag uint8, body []byte) {
	hdr := appendU32(nil, uint32(1+len(body)))
	_, _ = w.Write(hdr)
	_, _ = w.Write([]byte{tag})
	_, _ = w.Write(body)
}

// fakeBridgeRuntime implements just enough of ROS2Runtime: ExecROS2Stream runs an
// in-process "bridge" that echoes READY then, on SUBSCRIBE, emits one MESSAGE.
type fakeBridgeRuntime struct{ ROS2Runtime }

func (f *fakeBridgeRuntime) ExecROS2Stream(ctx context.Context, opts ROS2ExecOptions, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	// READY
	var ready []byte
	ready = foxglovebridge.AppendString(ready, "jazzy")
	ready = append(ready, 0)
	writeFrame(stdout, foxglovebridge.KindReady, ready)

	buf := make([]byte, 0, 64)
	for {
		fr, nb, err := foxglovebridge.ReadFrame(stdin, buf)
		buf = nb
		if err != nil {
			return 0, nil
		}
		if fr.Tag == foxglovebridge.OpSubscribe {
			// Reply with one MESSAGE for subID parsed from the body.
			subID := readU32(fr.Body)
			var m []byte
			m = appendU32(m, subID)
			m = appendU64(m, 42)
			m = append(m, 0xAB)
			writeFrame(stdout, foxglovebridge.KindMessage, m)
		}
	}
}

func TestBridgeSubscribeDeliversMessage(t *testing.T) {
	b := newROS2Bridge(&fakeBridgeRuntime{})
	sc := ros2SC{name: "sc", rmw: "rmw_cyclonedds_cpp", domainID: 0}
	ch, cancel, err := b.Subscribe(context.Background(), sc, "/t", "std_msgs/msg/String")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case m := <-ch:
		if m.TimestampNs != 42 || len(m.CDR) != 1 || m.CDR[0] != 0xAB {
			t.Fatalf("msg = %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message")
	}
}

// deadRuntime's ExecROS2Stream exits immediately without reading stdin or
// writing anything, so the bridge process pipe closes right away and the
// first Subscribe write fails, signaling the caller to fall back.
type deadRuntime struct{ ROS2Runtime }

func (d *deadRuntime) ExecROS2Stream(ctx context.Context, opts ROS2ExecOptions, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return 1, nil
}

func TestBridgeUnavailableReturnsError(t *testing.T) {
	// A runtime whose ExecROS2Stream exits immediately closes the pipe; the first
	// Subscribe write then fails, signaling the caller to fall back.
	b := newROS2Bridge(&deadRuntime{})
	_, _, err := b.Subscribe(context.Background(), ros2SC{name: "x"}, "/t", "T")
	if err == nil {
		t.Fatal("want fallback error when bridge is dead")
	}
}
