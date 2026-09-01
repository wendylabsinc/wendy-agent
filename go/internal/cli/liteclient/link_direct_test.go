package liteclient

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"
)

// noisyHandshakePort models a freshly rebooted board over USB Serial JTAG.
//
// backlog is console output the device queued before echo mode took effect;
// draining it one byte at a time keeps Read continuously ready, so the
// handshake never sees the idle read the pre-65df7e0e implementation waited
// for. backlogDelay paces that drain, letting the handshake's 100 ms resend
// timer fire while echoes are still stuck behind the backlog.
//
// swallow models the mode switch being applied asynchronously: that many
// sentinels are discarded unechoed (console mode does not echo host input)
// before echo mode goes live.
type noisyHandshakePort struct {
	backlog      int           // console bytes still to emit before any echo
	backlogDelay time.Duration // per-byte pacing while the backlog drains
	swallow      int           // sentinels discarded before echo mode is live

	sentinels    [][]byte // every sentinel written, in order
	rx           []byte   // echoes, queued behind the backlog
	modeSwitched bool
	rxAtSwitch   int // bytes still unread when DLE m was written
}

func (p *noisyHandshakePort) Write(data []byte) (int, error) {
	switch {
	case bytes.Equal(data, []byte{escapeChar, escapeChar, escapeChar, escapeChar, 'e'}):
		if len(p.sentinels) > 0 {
			return 0, errors.New("echo mode switch written after a sentinel")
		}
		return len(data), nil
	case bytes.Equal(data, []byte{escapeChar, 'm'}):
		p.modeSwitched = true
		p.rxAtSwitch = p.backlog + len(p.rx)
		return len(data), nil
	default:
		if len(data) != 48 {
			return 0, fmt.Errorf("unexpected handshake write of %d bytes", len(data))
		}
		p.sentinels = append(p.sentinels, append([]byte(nil), data[16:]...))
		if p.swallow > 0 {
			p.swallow--
			return len(data), nil
		}
		// Echo mode echoes every received byte, padding included.
		p.rx = append(p.rx, data...)
		return len(data), nil
	}
}

func (p *noisyHandshakePort) Read(dst []byte) (int, error) {
	if len(p.sentinels) == 0 {
		return 0, errors.New("handshake read before sending a sentinel")
	}
	if p.backlog > 0 {
		p.backlog--
		time.Sleep(p.backlogDelay)
		dst[0] = 'x'
		return 1, nil
	}
	if len(p.rx) == 0 {
		return 0, nil // read timeout: nothing pending
	}
	dst[0] = p.rx[0]
	p.rx = p.rx[1:]
	return 1, nil
}

func (*noisyHandshakePort) SetReadTimeout(time.Duration) error { return nil }

func TestSerialHandshakeSendsSentinelBeforeDrainingBootLogs(t *testing.T) {
	port := &noisyHandshakePort{backlog: 48}
	if err := serialHandshake(port); err != nil {
		t.Fatalf("serialHandshake() = %v", err)
	}
	if !port.modeSwitched {
		t.Fatal("handshake returned without switching to WendyCom mode")
	}
	if len(port.sentinels) != 1 {
		t.Errorf("sent %d sentinels, want 1", len(port.sentinels))
	}
}

// TestSerialHandshakeMatchesTheLastSentinelSent covers the guarantee the
// sentinel exists for: on return, the stream is drained and both sides are in
// sync. Matching any earlier sentinel leaves the later echoes queued, and the
// WendyCom framer reads their leading padding as a frame header.
func TestSerialHandshakeMatchesTheLastSentinelSent(t *testing.T) {
	port := &noisyHandshakePort{backlog: 25, backlogDelay: 10 * time.Millisecond}
	if err := serialHandshake(port); err != nil {
		t.Fatalf("serialHandshake() = %v", err)
	}
	if len(port.sentinels) < 2 {
		t.Fatalf("test did not exercise the race: only %d sentinel(s) sent", len(port.sentinels))
	}
	if !port.modeSwitched {
		t.Fatal("handshake returned without switching to WendyCom mode")
	}
	if port.rxAtSwitch != 0 {
		t.Errorf("%d unread byte(s) left in the stream at the mode switch, so an "+
			"earlier sentinel was matched", port.rxAtSwitch)
	}
	seen := make(map[string]bool, len(port.sentinels))
	for _, sentinel := range port.sentinels {
		if seen[string(sentinel)] {
			t.Fatalf("sentinel %q sent twice: a repeated sentinel cannot prove the stream drained", sentinel)
		}
		seen[string(sentinel)] = true
	}
}

// TestSerialHandshakeResendsAfterSwallowedSentinels covers the device applying
// the echo mode switch only after some sentinels have already been written and
// silently dropped.
func TestSerialHandshakeResendsAfterSwallowedSentinels(t *testing.T) {
	port := &noisyHandshakePort{swallow: 2}
	if err := serialHandshake(port); err != nil {
		t.Fatalf("serialHandshake() = %v", err)
	}
	if len(port.sentinels) != 3 {
		t.Errorf("sent %d sentinels, want 3 (two swallowed, one echoed)", len(port.sentinels))
	}
	if port.rxAtSwitch != 0 {
		t.Errorf("%d unread byte(s) left in the stream at the mode switch", port.rxAtSwitch)
	}
}
