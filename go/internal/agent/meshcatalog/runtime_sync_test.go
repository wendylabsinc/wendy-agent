package meshcatalog

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func TestRuntimePinnedTLS13PeerOrder(t *testing.T) {
	f := newFixture(t)
	serverCatalog, _ := f.newCatalog(t, p534, "default", nil, nil)
	server, _ := NewRuntime(serverCatalog, func() localmesh.NodeSnapshot { return localmesh.NodeSnapshot{} })
	for _, entry := range []struct {
		name, principal string
	}{{"lower", p533}, {"higher", p535}} {
		t.Run(entry.name, func(t *testing.T) {
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			_ = left.SetDeadline(time.Now().Add(5 * time.Second))
			_ = right.SetDeadline(time.Now().Add(5 * time.Second))
			serverResult := make(chan error, 1)
			go func() {
				err := tls.Server(right, server.serverTLS()).Handshake()
				if err != nil {
					_ = right.Close()
				}
				serverResult <- err
			}()
			config, err := f.creds[entry.principal].PeerTLS(principalOf(t, p534))
			if err != nil {
				t.Fatal(err)
			}
			config.NextProtos = []string{syncALPN}
			client := tls.Client(left, config)
			clientErr := client.Handshake()
			serverErr := <-serverResult
			if entry.principal == p533 {
				if clientErr != nil || serverErr != nil {
					t.Fatalf("valid mTLS: client=%v server=%v", clientErr, serverErr)
				}
				if state := client.ConnectionState(); state.Version != tls.VersionTLS13 || state.NegotiatedProtocol != syncALPN {
					t.Fatalf("TLS version/ALPN: %d %q", state.Version, state.NegotiatedProtocol)
				}
			} else if serverErr == nil {
				t.Fatal("wrong connector device admitted")
			}
		})
	}
}

func waitCatalog(t *testing.T, c *Catalog, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.Snapshot(time.Now())) == count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("catalog device %s has %d records, want %d", c.device, len(c.Snapshot(time.Now())), count)
}

func waitPeerGone(t *testing.T, r *Runtime, device meshid.Device) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		gone := r.peers[device] == nil
		r.mu.Unlock()
		if gone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("peer %s still registered", device.UUID())
}

func TestRuntimeThreeHopPartitionAndReconcile(t *testing.T) {
	f := newFixture(t)
	a, _ := f.newCatalog(t, p533, "default", nil, nil)
	b, _ := f.newCatalog(t, p534, "default", nil, nil)
	c, _ := f.newCatalog(t, p535, "default", nil, nil)
	da, db, dc := principalOf(t, p533), principalOf(t, p534), principalOf(t, p535)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	view := func() localmesh.NodeSnapshot { return localmesh.NodeSnapshot{} }
	ra, _ := NewRuntime(a, view)
	rb, _ := NewRuntime(b, view)
	rc, _ := NewRuntime(c, view)
	connect := func(left *Runtime, leftPeer meshid.Device, right *Runtime, rightPeer meshid.Device) (net.Conn, net.Conn) {
		l, r := net.Pipe()
		go left.session(ctx, leftPeer, l)
		go right.session(ctx, rightPeer, r)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			left.mu.Lock()
			leftReady := left.peers[leftPeer] != nil
			left.mu.Unlock()
			right.mu.Lock()
			rightReady := right.peers[rightPeer] != nil
			right.mu.Unlock()
			if leftReady && rightReady {
				return l, r
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("test peer session did not start")
		return nil, nil
	}
	ab1, ab2 := connect(ra, db, rb, da)
	defer ab1.Close()
	defer ab2.Close()
	bc1, bc2 := connect(rb, dc, rc, db)
	defer bc1.Close()
	defer bc2.Close()
	w, err := a.Publish(testSpec(), f.now)
	if err != nil {
		t.Fatal(err)
	}
	ra.Broadcast(w)
	waitCatalog(t, c, 1)
	_ = bc1.Close()
	_ = bc2.Close()
	waitPeerGone(t, rb, dc)
	waitPeerGone(t, rc, db)
	tombstone, err := a.Remove("com.wendy.test", "http", f.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ra.Broadcast(tombstone)
	waitCatalog(t, b, 0)
	if len(c.Snapshot(time.Now())) != 1 {
		t.Fatal("partitioned third agent lost service before reconciliation")
	}
	bc3, bc4 := connect(rb, dc, rc, db)
	defer bc3.Close()
	defer bc4.Close()
	waitCatalog(t, c, 0)
}

// Force a write failure while the reader is blocked handing off decoded
// records. The runtime itself stays alive, as it would across peer reconnects.
type failedCatalogWriter struct {
	net.Conn
	fail    <-chan struct{}
	started chan struct{}
	once    sync.Once
}

func (c *failedCatalogWriter) Write([]byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	<-c.fail
	return 0, errors.New("test peer write failed")
}
func TestRuntimeFailedSessionJoinsBackloggedReader(t *testing.T) {
	f := newFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	receiver, cache := f.newCatalog(t, p533, "default", nil, nil)
	// V1 sends records without an initial manifest. Seed a real outbound
	// record before blocking incoming persistence so both protocol versions
	// start the failing writer independently of reader progress.
	if _, err := receiver.Publish(testSpec(), f.now); err != nil {
		t.Fatal(err)
	}
	receiver.persist = func([]Receipt) error {
		once.Do(func() { close(entered); <-release })
		return nil
	}
	origin, _ := f.newCatalog(t, p535, "default", nil, nil)
	if _, err := cache.Put(f.creds[p535].Certificate.Certificate, f.now); err != nil {
		t.Fatal(err)
	}
	record, err := origin.Publish(testSpec(), f.now)
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := NewRuntime(receiver, func() localmesh.NodeSnapshot { return localmesh.NodeSnapshot{} })
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fail := make(chan struct{})
	started := make(chan struct{})
	var releaseOnce, failOnce sync.Once
	unblock := func() { failOnce.Do(func() { close(fail) }); releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		runtime.session(ctx, principalOf(t, p535), &failedCatalogWriter{Conn: left, fail: fail, started: started})
	}()
	// Do not let a racing incoming Accept block the session select before
	// it queues that first write; then deliberately fill its read backlog.
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("writer started")
	}
	sent := make(chan struct{})
	go func() {
		// One record is in Accept, sixteen are queued and the eighteenth is
		// decoded by the reader, which cannot enqueue it until Accept returns.
		for i := 0; ; i++ {
			if WriteMessage(right, Message{Kind: "record", Record: &record}) != nil {
				return
			}
			if i == 17 {
				close(sent)
			}
		}
	}()
	for _, phase := range []struct {
		name string
		done <-chan struct{}
	}{
		{"writer started", started}, {"accept blocked", entered}, {"reader queue filled", sent},
	} {
		select {
		case <-phase.done:
		case <-time.After(3 * time.Second):
			t.Fatal(phase.name)
		}
	}
	failOnce.Do(func() { close(fail) })
	// Keep Accept blocked while the writer reports its failure.
	time.Sleep(40 * time.Millisecond)
	releaseOnce.Do(func() { close(release) })
	select {
	case <-sessionDone:
	case <-time.After(3 * time.Second):
		t.Fatal("failed session retained its reader while runtime stayed alive")
	}
	if ctx.Err() != nil {
		t.Fatal("session cancelled the runtime context")
	}
}
