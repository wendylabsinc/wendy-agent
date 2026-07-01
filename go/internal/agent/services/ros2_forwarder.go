package services

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ros2ForwarderScript returns a self-contained rclpy program (run via
// `python3 -c "<script>" <topic>`) that raw-subscribes one topic and writes each
// message to stdout as a length-framed binary CDR record:
//
//	[uint32 little-endian length][raw CDR bytes]
//
// This replaces `ros2 topic echo --raw`, whose Python-bytes text repr inflates
// every message ~4x and costs a byte-by-byte decode on the (often weakest) CPU
// in the system. Here the serialized CDR bytes DDS already produced are written
// straight through — no text, no decode — which is what Foxglove consumes as-is
// (encoding=cdr). The topic is passed as argv[1] via ExecROS2's "$@" indirection,
// never string-interpolated, so this script contains no untrusted input.
//
// QoS is best-effort, KEEP_LAST depth 1 to match sensor publishers (a reliable
// subscriber will not receive from a best-effort publisher) and to favour the
// freshest sample under load.
func ros2ForwarderScript() string {
	return `
import sys, struct
import rclpy
from rclpy.node import Node
from rclpy.qos import QoSProfile, ReliabilityPolicy, HistoryPolicy
from ros2topic.api import get_msg_class

topic = sys.argv[1]
rclpy.init(args=None)
node = Node('wendy_foxglove_forward')
# Resolve the message type from the live graph, waiting for a publisher.
msg_cls = get_msg_class(node, topic, blocking=True, include_hidden_topics=True)
if msg_cls is None:
    sys.stderr.write('wendy-forward: could not resolve message type for %s\n' % topic)
    sys.exit(2)
qos = QoSProfile(depth=1, reliability=ReliabilityPolicy.BEST_EFFORT, history=HistoryPolicy.KEEP_LAST)
out = sys.stdout.buffer
def cb(raw):
    # raw is the serialized (CDR) message because the subscription is raw=True.
    out.write(struct.pack('<I', len(raw)))
    out.write(raw)
    out.flush()
node.create_subscription(msg_cls, topic, cb, qos, raw=True)
try:
    rclpy.spin(node)
except KeyboardInterrupt:
    pass
`
}

// readCDRFrames reads consecutive [uint32 LE length][payload] frames from r,
// invoking emit for each payload. The payload slice is reused across calls, so
// emit must copy any bytes it needs to retain beyond the call. Returns nil on a
// clean EOF at a frame boundary, or an error on a truncated frame or when emit
// returns an error.
func readCDRFrames(r io.Reader, emit func(cdr []byte) error) error {
	var hdr [4]byte
	buf := make([]byte, 0, 1<<20)
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				return nil // clean end at a frame boundary
			}
			if err == io.ErrUnexpectedEOF {
				return fmt.Errorf("truncated frame header")
			}
			return err
		}
		n := binary.LittleEndian.Uint32(hdr[:])
		if uint32(cap(buf)) < n {
			buf = make([]byte, n)
		}
		buf = buf[:n]
		if _, err := io.ReadFull(r, buf); err != nil {
			return fmt.Errorf("truncated frame payload (want %d bytes): %w", n, err)
		}
		if err := emit(buf); err != nil {
			return err
		}
	}
}
