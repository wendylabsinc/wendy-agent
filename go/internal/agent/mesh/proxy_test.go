package mesh

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// halfClosablePipe wraps one end of a net.Pipe to give it a CloseWrite
// method, since net.Conn from net.Pipe does not implement half-close on its
// own. CloseWrite signals writeClosed so the peer side of the fake can
// distinguish "no more data is coming" from a live read.
type halfClosablePipe struct {
	net.Conn
	writeClosed chan struct{}
}

func newHalfClosablePipe(c net.Conn) *halfClosablePipe {
	return &halfClosablePipe{Conn: c, writeClosed: make(chan struct{})}
}

func (h *halfClosablePipe) CloseWrite() error {
	close(h.writeClosed)
	return nil
}

// fakeDialer records the dial and hands back one end of a pipe whose other
// end echoes with a prefix.
type fakeDialer struct {
	gotDevice int32
	gotPort   uint16
	err       error

	// plainPipe, when set, makes DialDevice return the bare net.Pipe conn
	// (no CloseWrite) instead of wrapping it in halfClosablePipe. The
	// fake's remote goroutine then has no half-close signal available and
	// can only observe completion via a full Close of its peer - exactly
	// the scenario relayBytes must handle without leaking.
	plainPipe bool

	// closed, when non-nil and plainPipe is set, receives the error
	// observed by the fake's second Read on its pipe end once (if ever)
	// the local peer conn is torn down.
	closed chan error

	// mode, when set, is returned as DialDevice's mode result. Defaults to
	// "lan-direct" so existing tests that don't care about it still get a
	// realistic non-empty value.
	mode string
}

func (f *fakeDialer) DialDevice(_ context.Context, deviceID int32, port uint16) (net.Conn, string, error) {
	f.gotDevice, f.gotPort = deviceID, port
	if f.err != nil {
		return nil, "", f.err
	}
	mode := f.mode
	if mode == "" {
		mode = "lan-direct"
	}
	a, b := net.Pipe()
	if f.plainPipe {
		go func() {
			buf := make([]byte, 64)
			_, _ = b.Read(buf) // drain the client's message; a has no CloseWrite.
			// A bare net.Pipe end has no half-close signal, so the only
			// way this goroutine can learn "the local side is done
			// sending" is a full Close of a, which unblocks this
			// pending Read with io.EOF. If relayBytes never closes a
			// (the bug under test), this blocks forever.
			_, err := b.Read(buf)
			if f.closed != nil {
				f.closed <- err
			}
		}()
		return a, mode, nil
	}

	hcp := newHalfClosablePipe(a)
	go func() {
		buf := make([]byte, 64)
		n, _ := b.Read(buf)
		fmt.Fprintf(b, "peer:%s", buf[:n])
		// Wait for the relay to signal (via CloseWrite -> writeClosed)
		// that no more data is coming from the client before we close
		// our end, so the response above is guaranteed to have been
		// written first. This exercises the CloseWrite path
		// deterministically instead of racing on a sleep.
		<-hcp.writeClosed
		b.Close()
	}()
	return hcp, mode, nil
}

// fakeConnMetrics records RecordBytes calls for assertions, without needing
// the real services.MeshMetrics (which this package cannot import — see the
// ConnMetrics doc comment in proxy.go).
type fakeConnMetrics struct {
	mu    sync.Mutex
	bytes map[string]int64 // key: "<peer>:<dir>"
}

func newFakeConnMetrics() *fakeConnMetrics {
	return &fakeConnMetrics{bytes: make(map[string]int64)}
}

func (f *fakeConnMetrics) RecordBytes(peer int32, dir string, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bytes[fmt.Sprintf("%d:%s", peer, dir)] += n
}

func (f *fakeConnMetrics) get(peer int32, dir string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bytes[fmt.Sprintf("%d:%s", peer, dir)]
}

func startTestProxy(t *testing.T, d PeerDialer, m ConnMetrics, dst netip.AddrPort) net.Addr {
	t.Helper()
	p := NewProxy(zap.NewNop(), d, m)
	p.origDst = func(*net.TCPConn) (netip.AddrPort, error) { return dst, nil }
	if err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p.Addr()
}

func TestProxyRelaysToDialedPeer(t *testing.T) {
	d := &fakeDialer{}
	fm := newFakeConnMetrics()
	addr := startTestProxy(t, d, fm, netip.MustParseAddrPort("10.99.0.215:8080"))

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	conn.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "peer:hello" {
		t.Fatalf("relayed %q, want %q", got, "peer:hello")
	}
	if d.gotDevice != 215 || d.gotPort != 8080 {
		t.Fatalf("dialed device %d port %d, want 215/8080", d.gotDevice, d.gotPort)
	}

	// handleConn records byte metrics after relayBytes returns, which can
	// land slightly after the client's own io.ReadAll returns; poll briefly
	// instead of asserting immediately.
	wantTx, wantRx := int64(len("hello")), int64(len("peer:hello"))
	deadline := time.After(2 * time.Second)
	for {
		tx, rx := fm.get(215, "tx"), fm.get(215, "rx")
		if tx == wantTx && rx == wantRx {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("byte metrics = tx:%d rx:%d, want tx:%d rx:%d", tx, rx, wantTx, wantRx)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestProxyFullClosesNonCloseWritePeer is the regression test for a leak:
// when the peer conn does not implement CloseWrite (e.g. a bare net.Pipe
// end, as the upcoming PeerDialer returns for tunnel conns), the relay must
// still fully close it once the opposite direction finishes, or the far end
// of the pipe blocks on read forever and the relay goroutines/FDs never
// exit.
func TestProxyFullClosesNonCloseWritePeer(t *testing.T) {
	d := &fakeDialer{plainPipe: true, closed: make(chan error, 1)}
	addr := startTestProxy(t, d, nil, netip.MustParseAddrPort("10.99.0.215:8080"))

	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	conn.(*net.TCPConn).CloseWrite()

	select {
	case err := <-d.closed:
		if err != io.EOF && err != io.ErrClosedPipe {
			t.Fatalf("peer far end observed %v on close, want io.EOF or io.ErrClosedPipe", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay leaked the peer conn: far end never observed a close for a non-CloseWrite peer")
	}
}

func TestProxyClosesOnNonMeshVIP(t *testing.T) {
	d := &fakeDialer{}
	addr := startTestProxy(t, d, nil, netip.MustParseAddrPort("192.168.1.1:80"))
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != io.EOF {
		t.Fatalf("expected EOF for non-mesh destination, got %v", err)
	}
	if d.gotDevice != 0 {
		t.Fatal("dialer must not be called for a non-mesh destination")
	}
}

// TestRelayBytesReturnsByteCountsPerDirection drives relayBytes directly
// (bypassing Proxy) over two net.Pipe pairs and checks the returned tx/rx
// counts match what was actually written in each direction — the
// io.Copy-return-value plumbing Proxy.handleConn now depends on for
// mesh.bytes and the per-connection close log.
func TestRelayBytesReturnsByteCountsPerDirection(t *testing.T) {
	a, aOther := net.Pipe() // "a" is the client-facing conn relayBytes reads/writes
	b, bOther := net.Pipe() // "b" is the peer-facing conn relayBytes reads/writes

	clientPayload := []byte("hello-from-client")
	peerPayload := []byte("hi-from-the-peer!!")

	txGot := make([]byte, len(clientPayload))
	rxGot := make([]byte, len(peerPayload))
	txReceived := make(chan struct{})
	rxReceived := make(chan struct{})

	go func() {
		_, _ = io.ReadFull(bOther, txGot) // bOther observes what relayBytes copies a->b (tx)
		close(txReceived)
	}()
	go func() {
		_, _ = io.ReadFull(aOther, rxGot) // aOther observes what relayBytes copies b->a (rx)
		close(rxReceived)
	}()
	go func() { _, _ = aOther.Write(clientPayload) }()
	go func() { _, _ = bOther.Write(peerPayload) }()

	relayDone := make(chan struct{})
	var txBytes, rxBytes int64
	go func() {
		txBytes, rxBytes = relayBytes(a, b)
		close(relayDone)
	}()

	// Wait for both payloads to be fully delivered before closing either
	// end: relayBytes fully closes its counterpart conn once one direction
	// hits EOF (neither a nor b implements CloseWrite here), and closing
	// too early would race with and truncate the still in-flight opposite
	// direction.
	select {
	case <-txReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("tx payload never fully relayed")
	}
	select {
	case <-rxReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("rx payload never fully relayed")
	}
	aOther.Close()
	bOther.Close()

	select {
	case <-relayDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relayBytes did not return after both ends closed")
	}

	if txBytes != int64(len(clientPayload)) {
		t.Fatalf("tx = %d, want %d", txBytes, len(clientPayload))
	}
	if rxBytes != int64(len(peerPayload)) {
		t.Fatalf("rx = %d, want %d", rxBytes, len(peerPayload))
	}
	if string(txGot) != string(clientPayload) {
		t.Fatalf("peer received %q, want %q", txGot, clientPayload)
	}
	if string(rxGot) != string(peerPayload) {
		t.Fatalf("client received %q, want %q", rxGot, peerPayload)
	}
}

// TestProxyLogsStructuredFieldsOnSuccess proves the per-connection close log
// carries the exact field keys the dashboard's Mesh tab filters/reads:
// mesh.peer, mesh.mode, mesh.result, mesh.bytes_tx, mesh.bytes_rx,
// mesh.duration_ms.
func TestProxyLogsStructuredFieldsOnSuccess(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	d := &fakeDialer{mode: "relay"}
	fm := newFakeConnMetrics()
	p := NewProxy(logger, d, fm)
	p.origDst = func(*net.TCPConn) (netip.AddrPort, error) {
		return netip.MustParseAddrPort("10.99.0.215:8080"), nil
	}
	if err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	conn, err := net.Dial("tcp", p.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	conn.(*net.TCPConn).CloseWrite()
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatal(err)
	}

	entry := waitForLogEntry(t, logs, "mesh connection")
	ctxMap := entry.ContextMap()
	if ctxMap["mesh.peer"] != "215" {
		t.Fatalf("mesh.peer = %v, want 215", ctxMap["mesh.peer"])
	}
	if ctxMap["mesh.mode"] != "relay" {
		t.Fatalf("mesh.mode = %v, want relay", ctxMap["mesh.mode"])
	}
	if ctxMap["mesh.result"] != "ok" {
		t.Fatalf("mesh.result = %v, want ok", ctxMap["mesh.result"])
	}
	for _, key := range []string{"mesh.port", "mesh.bytes_tx", "mesh.bytes_rx", "mesh.duration_ms"} {
		if _, ok := ctxMap[key]; !ok {
			t.Fatalf("%s field missing from log entry: %+v", key, ctxMap)
		}
	}
}

// TestProxyLogsStructuredFieldsOnDialFailure proves a failed dial gets its
// own structured warning (mesh.peer/mesh.mode/mesh.port + the error) rather
// than the success-shaped "mesh connection" log with mesh.result=error —
// the dialer already recorded the mesh.connections error metric, so this log
// is the only per-attempt signal for the dashboard.
func TestProxyLogsStructuredFieldsOnDialFailure(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	logger := zap.New(core)
	d := &fakeDialer{err: fmt.Errorf("boom")}
	p := NewProxy(logger, d, nil)
	p.origDst = func(*net.TCPConn) (netip.AddrPort, error) {
		return netip.MustParseAddrPort("10.99.0.215:8080"), nil
	}
	if err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	conn, err := net.Dial("tcp", p.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	entry := waitForLogEntry(t, logs, "mesh connection failed")
	ctxMap := entry.ContextMap()
	if ctxMap["mesh.peer"] != "215" {
		t.Fatalf("mesh.peer = %v, want 215", ctxMap["mesh.peer"])
	}
	if ctxMap["mesh.mode"] != "" {
		t.Fatalf("mesh.mode = %v, want empty on dial error", ctxMap["mesh.mode"])
	}
	if _, ok := ctxMap["error"]; !ok {
		t.Fatalf("error field missing from log entry: %+v", ctxMap)
	}
}

// waitForLogEntry polls the observer for a log with the given message,
// since the proxy handles connections on its own goroutine.
func waitForLogEntry(t *testing.T, logs *observer.ObservedLogs, message string) observer.LoggedEntry {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		all := logs.FilterMessage(message).All()
		if len(all) > 0 {
			return all[0]
		}
		select {
		case <-deadline:
			t.Fatalf("no %q log observed", message)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
