package services

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestIsSuspectedPMTUBlackhole(t *testing.T) {
	handshakeEOF := errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: EOF"`)
	handshakeDeadline := errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: context deadline exceeded"`)
	certRejection := errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: remote error: tls: bad certificate"`)
	connRefused := errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: Error while dialing: dial tcp 1.2.3.4:50052: connect: connection refused"`)

	cases := []struct {
		name         string
		err          error
		tcpConnected bool
		want         bool
	}{
		{"handshake EOF after TCP connect", handshakeEOF, true, true},
		{"handshake deadline after TCP connect", handshakeDeadline, true, true},
		{"handshake EOF without TCP connect", handshakeEOF, false, false},
		{"cert rejection is not a black hole", certRejection, true, false},
		{"connection refused is not a black hole", connRefused, true, false},
		{"nil error", nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSuspectedPMTUBlackhole(tc.err, tc.tcpConnected); got != tc.want {
				t.Fatalf("isSuspectedPMTUBlackhole(%v, %v) = %v, want %v", tc.err, tc.tcpConnected, got, tc.want)
			}
		})
	}
}

func TestDialObserverRecordsTCPConnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	obs := &dialObserver{}
	if obs.TCPConnected() {
		t.Fatal("fresh observer reports TCPConnected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := obs.DialContext(ctx, ln.Addr().String())
	if err != nil {
		t.Fatalf("dial to local listener failed: %v", err)
	}
	conn.Close()
	if !obs.TCPConnected() {
		t.Fatal("observer did not record successful TCP connect")
	}

	obs.Reset()
	if obs.TCPConnected() {
		t.Fatal("Reset did not clear TCPConnected")
	}

	// A failed dial must not set the flag.
	ln.Close()
	if _, err := obs.DialContext(ctx, ln.Addr().String()); err == nil {
		t.Fatal("expected dial to closed listener to fail")
	}
	if obs.TCPConnected() {
		t.Fatal("failed dial recorded TCPConnected")
	}
}

func TestNotePMTUSuspicionLogsOnceAfterTwoConsecutiveFailures(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	c := NewTunnelBrokerClient(zap.New(core), "broker.example:50052", 1, 2, "", "", "", 0)

	handshakeEOF := errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: EOF"`)
	pmtuLogs := func() int {
		n := 0
		for _, entry := range logs.All() {
			for _, f := range entry.Context {
				if f.Key == "event" && f.String == "broker_suspected_pmtu_blackhole" {
					n++
				}
			}
		}
		return n
	}

	c.dialObs.markConnected()
	c.notePMTUSuspicion(handshakeEOF)
	if got := pmtuLogs(); got != 0 {
		t.Fatalf("logged after a single failure: %d entries", got)
	}

	c.dialObs.markConnected()
	c.notePMTUSuspicion(handshakeEOF)
	if got := pmtuLogs(); got != 1 {
		t.Fatalf("expected exactly one PMTU log after two consecutive failures, got %d", got)
	}

	// Further consecutive failures do not spam the log.
	c.dialObs.markConnected()
	c.notePMTUSuspicion(handshakeEOF)
	if got := pmtuLogs(); got != 1 {
		t.Fatalf("expected no additional PMTU logs on later failures, got %d", got)
	}

	// A non-black-hole failure resets the streak, so it takes two more
	// classified failures before the next log.
	c.notePMTUSuspicion(errors.New("connection refused"))
	c.dialObs.markConnected()
	c.notePMTUSuspicion(handshakeEOF)
	if got := pmtuLogs(); got != 1 {
		t.Fatalf("streak did not reset on unrelated failure, got %d logs", got)
	}
	c.dialObs.markConnected()
	c.notePMTUSuspicion(handshakeEOF)
	if got := pmtuLogs(); got != 2 {
		t.Fatalf("expected second PMTU log after new streak of two, got %d", got)
	}
}
