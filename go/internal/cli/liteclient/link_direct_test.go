package liteclient

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// noisyHandshakePort models a freshly rebooted board whose boot/app logs keep
// Read continuously ready. The sentinel echo is queued only after the host
// sends it; trying to read before that fails the test immediately.
type noisyHandshakePort struct {
	rx       []byte
	writes   int
	sentinel []byte
}

func (p *noisyHandshakePort) Write(data []byte) (int, error) {
	p.writes++
	switch p.writes {
	case 1:
		want := []byte{escapeChar, escapeChar, escapeChar, escapeChar, 'e'}
		if !bytes.Equal(data, want) {
			return 0, errors.New("unexpected mode-switch write")
		}
		return len(data), nil
	case 2:
		if len(data) != 48 {
			return 0, errors.New("unexpected sentinel write length")
		}
		p.sentinel = append([]byte(nil), data[16:]...)
		p.rx = append([]byte("boot log traffic that never provides an idle read"), p.sentinel...)
		return len(data), nil
	case 3:
		if !bytes.Equal(data, []byte{escapeChar, 'm'}) {
			return 0, errors.New("unexpected WendyCom mode-switch write")
		}
		return len(data), nil
	default:
		return 0, errors.New("unexpected extra handshake write")
	}
}

func (p *noisyHandshakePort) Read(dst []byte) (int, error) {
	if len(p.sentinel) == 0 {
		return 0, errors.New("handshake read before sending sentinel")
	}
	if len(p.rx) == 0 {
		return 0, errors.New("sentinel echo was not detected")
	}
	dst[0] = p.rx[0]
	p.rx = p.rx[1:]
	return 1, nil
}

func (*noisyHandshakePort) SetReadTimeout(time.Duration) error { return nil }

func TestSerialHandshakeSendsSentinelBeforeDrainingBootLogs(t *testing.T) {
	port := &noisyHandshakePort{}
	if err := serialHandshake(port); err != nil {
		t.Fatalf("serialHandshake() = %v", err)
	}
	if port.writes != 3 { // mode switch, sentinel, WendyCom mode switch
		t.Fatalf("writes = %d, want 3", port.writes)
	}
}
