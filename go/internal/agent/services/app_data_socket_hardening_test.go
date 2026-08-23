package services

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
)

// newTestDataSocketManager returns a manager rooted at a throwaway socket
// directory, with peer-credential verification neutralized (fail open) so the
// rate-limit and record-kind tests exercise only the behavior they target.
func newTestDataSocketManager(t *testing.T) *AppDataSocketManager {
	t.Helper()
	capture, err := data.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	socketRoot, err := os.MkdirTemp("/tmp", "wendy-data-hardening-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketRoot) })
	oldRoot := AppDataSocketRootPath
	AppDataSocketRootPath = socketRoot
	t.Cleanup(func() { AppDataSocketRootPath = oldRoot })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := NewAppDataSocketManager(ctx, nil, capture)
	m.peerCred = func(net.Conn) (peerCredentials, error) {
		return peerCredentials{}, errors.New("test: peer credentials unavailable")
	}
	return m
}

func sendRecord(t *testing.T, conn net.Conn, record data.ApplicationRecord) dataAck {
	t.Helper()
	body, _ := json.Marshal(record)
	if err := writeDataFrame(conn, json.RawMessage(body)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	ackBody, err := readDataFrame(conn)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	var ack dataAck
	if err := json.Unmarshal(ackBody, &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	return ack
}

// TestAppDataRateLimitIsPerAppNotPerConnection proves a second connection for
// the same app draws from the same rate budget instead of getting a fresh one.
func TestAppDataRateLimitIsPerAppNotPerConnection(t *testing.T) {
	m := newTestDataSocketManager(t)
	// A deterministic 2-record budget that does not refill during the test.
	m.newLimiter = func() *notificationRateLimiter {
		l := newNotificationRateLimiter(2, time.Hour)
		return &l
	}
	dir, err := m.Ensure("com.example.a", "")
	if err != nil {
		t.Fatal(err)
	}
	sockPath := filepath.Join(dir, DataSocketFilename)

	conn1, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.Close()
	rec := data.ApplicationRecord{Version: 1, Type: "event", Name: "ready", ClientBootID: "unavailable"}
	if ack := sendRecord(t, conn1, rec); ack.State != "buffered" {
		t.Fatalf("conn1 record 1: state = %q, want buffered", ack.State)
	}
	if ack := sendRecord(t, conn1, rec); ack.State != "buffered" {
		t.Fatalf("conn1 record 2: state = %q, want buffered", ack.State)
	}

	// The per-app budget is now exhausted; a brand-new connection must not get
	// its own allotment.
	conn2, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	if ack := sendRecord(t, conn2, rec); ack.State != "rejected" || ack.Error != "rate limit exceeded" {
		t.Fatalf("conn2 record: ack = %+v, want rejected/rate limit exceeded (budget must be shared)", ack)
	}
}

// TestAppDataUnknownKindRejectedNotFatal shows an unknown record kind gets a
// clean rejected ack and does not tear down the connection.
func TestAppDataUnknownKindRejectedNotFatal(t *testing.T) {
	m := newTestDataSocketManager(t)
	dir, err := m.Ensure("com.example.a", "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, DataSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ack := sendRecord(t, conn, data.ApplicationRecord{Version: 1, Type: "telemetry", Name: "x", ClientBootID: "unavailable"})
	if ack.State != "rejected" {
		t.Fatalf("unknown kind: state = %q, want rejected", ack.State)
	}

	// The connection survives: a valid record on the same connection is still
	// accepted.
	ack = sendRecord(t, conn, data.ApplicationRecord{Version: 1, Type: "event", Name: "ready", ClientBootID: "unavailable"})
	if ack.State != "buffered" {
		t.Fatalf("valid record after unknown kind: state = %q, want buffered (connection must survive)", ack.State)
	}
}

// TestAppDataPeerCredentialMismatchRefused injects a peer whose cgroup names a
// different app and asserts the connection is refused.
func TestAppDataPeerCredentialMismatchRefused(t *testing.T) {
	m := newTestDataSocketManager(t)
	m.peerCred = func(net.Conn) (peerCredentials, error) {
		return peerCredentials{UID: 0, PID: 4242}, nil
	}
	m.cgroupOfPID = func(int32) (string, error) {
		return "0::/system.slice/edge-agent-com.example.other.scope\n", nil
	}
	dir, err := m.Ensure("com.example.a", "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, DataSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ackBody, err := readDataFrame(conn)
	if err != nil {
		t.Fatalf("expected a rejection ack, got read error: %v", err)
	}
	var ack dataAck
	if err := json.Unmarshal(ackBody, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.State != "rejected" {
		t.Fatalf("mismatched peer: state = %q, want rejected", ack.State)
	}
}

// TestAppDataPeerCredentialMatchAllowed shows a peer whose cgroup names the
// socket's own app is admitted.
func TestAppDataPeerCredentialMatchAllowed(t *testing.T) {
	m := newTestDataSocketManager(t)
	m.peerCred = func(net.Conn) (peerCredentials, error) {
		return peerCredentials{UID: 0, PID: 4242}, nil
	}
	m.cgroupOfPID = func(int32) (string, error) {
		return "0::/system.slice/edge-agent-com.example.a@worker.scope\n", nil
	}
	dir, err := m.Ensure("com.example.a", "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, DataSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ack := sendRecord(t, conn, data.ApplicationRecord{Version: 1, Type: "event", Name: "ready", ClientBootID: "unavailable"})
	if ack.State != "buffered" {
		t.Fatalf("matching peer (service suffix stripped): state = %q, want buffered", ack.State)
	}
}

// readRejectAck reads a single ack frame off conn and fails unless it is a
// rejection. Used by the fail-closed peer-credential tests below.
func readRejectAck(t *testing.T, conn net.Conn) dataAck {
	t.Helper()
	ackBody, err := readDataFrame(conn)
	if err != nil {
		t.Fatalf("expected a rejection ack, got read error: %v", err)
	}
	var ack dataAck
	if err := json.Unmarshal(ackBody, &ack); err != nil {
		t.Fatal(err)
	}
	return ack
}

// TestAppDataPeerCredentialUnreadableCgroupRefused proves the accept-to-/proc
// TOCTOU is closed. Once SO_PEERCRED yields a pid, a cgroup that cannot be read
// (the peer exited between accept and the /proc read, or its pid was recycled)
// must be REFUSED, not admitted. Fail-open here would let a peer that buffered
// forged records and then exited have those records attributed to this app.
func TestAppDataPeerCredentialUnreadableCgroupRefused(t *testing.T) {
	m := newTestDataSocketManager(t)
	m.peerCred = func(net.Conn) (peerCredentials, error) {
		return peerCredentials{UID: 0, PID: 4242}, nil
	}
	m.cgroupOfPID = func(int32) (string, error) {
		// The peer has exited by the time verifyPeer reads /proc/<pid>/cgroup.
		return "", os.ErrNotExist
	}
	dir, err := m.Ensure("com.example.a", "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, DataSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if ack := readRejectAck(t, conn); ack.State != "rejected" {
		t.Fatalf("unreadable peer cgroup: state = %q, want rejected (fail closed)", ack.State)
	}
}

// TestAppDataPeerCredentialNoScopeRefused proves a peer that resolves to no
// wendy app scope (a host process, or a recycled pid now held by one) is
// refused once its credentials are known, rather than admitted under the
// socket's app identity.
func TestAppDataPeerCredentialNoScopeRefused(t *testing.T) {
	m := newTestDataSocketManager(t)
	m.peerCred = func(net.Conn) (peerCredentials, error) {
		return peerCredentials{UID: 0, PID: 4242}, nil
	}
	m.cgroupOfPID = func(int32) (string, error) {
		return "0::/user.slice/user-1000.slice/session-3.scope\n", nil
	}
	dir, err := m.Ensure("com.example.a", "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, DataSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if ack := readRejectAck(t, conn); ack.State != "rejected" {
		t.Fatalf("no-scope peer: state = %q, want rejected (fail closed)", ack.State)
	}
}

// TestAppIDFromCgroup covers the cgroup identity extraction directly.
func TestAppIDFromCgroup(t *testing.T) {
	cases := []struct {
		name    string
		cgroup  string
		want    string
		present bool
	}{
		{"single service", "0::/system.slice/edge-agent-com.example.a.scope\n", "com.example.a", true},
		{"multi service strips suffix", "0::/system.slice/edge-agent-com.example.a@worker.scope\n", "com.example.a", true},
		{"non-wendy process", "0::/user.slice/user-1000.slice/session-3.scope\n", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := appIDFromCgroup(tc.cgroup)
			if ok != tc.present || got != tc.want {
				t.Fatalf("appIDFromCgroup(%q) = (%q, %v), want (%q, %v)", tc.cgroup, got, ok, tc.want, tc.present)
			}
		})
	}
}
