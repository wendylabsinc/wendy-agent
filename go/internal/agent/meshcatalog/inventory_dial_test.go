package meshcatalog

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
)

func TestInventoryColdRetryConsumesActualTCPAndTLSFailures(t *testing.T) {
	for _, tlsFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "TCP refused", true: "TLS wrong pinned asset"}[tlsFailure], func(t *testing.T) {
			f := newFixture(t)
			c, _ := f.newCatalog(t, p533, "default", nil, nil)
			view := bridgeRouteView(t, f.now, p535)
			r, _ := NewRuntime(c, func() localmesh.NodeSnapshot { return view })
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			address := listener.Addr().String()
			serverDone := make(chan error, 1)
			if tlsFailure {
				// A real TLS handshake presents asset534 while the client is pinned535.
				wrong, _ := f.newCatalog(t, p534, "default", nil, nil)
				server, _ := NewRuntime(wrong, func() localmesh.NodeSnapshot { return localmesh.NodeSnapshot{} })
				go func() {
					raw, e := listener.Accept()
					if e != nil {
						serverDone <- e
						return
					}
					defer raw.Close()
					_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
					serverDone <- tls.Server(raw, server.serverTLS()).Handshake()
				}()
			} else {
				_ = listener.Close()
			}
			var dials atomic.Int32
			r.dialPeer = func(ctx context.Context, self, peer netip.Addr) (net.Conn, error) {
				dials.Add(1)
				return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
			}
			r.inventoryFailed(f.creds[p535].Device, false, time.Now()) // one stale-inventory cold retry owed
			r.outbound(context.Background(), f.creds[p535].Device)
			if dials.Load() != 1 {
				t.Fatalf("dials=%d", dials.Load())
			}
			if tlsFailure {
				select {
				case e := <-serverDone:
					if e == nil {
						t.Fatal("wrong peer TLS unexpectedly succeeded")
					}
				case <-time.After(time.Second):
					t.Fatal("TLS server did not finish")
				}
			}
			if _, allowed := r.inventoryAttempt(f.creds[p535].Device, time.Now()); allowed {
				t.Fatal("failed TCP/TLS left a reusable cold attempt")
			}
			// Both direct outbound entry and the periodic discover path must avoid a
			// connection, not merely reject inventory after completing another TLS handshake.
			r.outbound(context.Background(), f.creds[p535].Device)
			r.discover(context.Background(), time.Now())
			time.Sleep(30 * time.Millisecond)
			if dials.Load() != 1 {
				t.Fatal("cooldown still made TCP/TLS connections")
			}
			r.mu.Lock()
			inflight := len(r.dialing)
			r.mu.Unlock()
			if inflight != 0 {
				t.Fatal("cooldown scheduled a dial goroutine")
			}
		})
	}
}

func TestInventoryInboundCooldownRejectsApplicationBeforeInventory(t *testing.T) {
	f := newFixture(t)
	c, _ := f.newCatalog(t, p535, "default", nil, nil)
	r, _ := NewRuntime(c, func() localmesh.NodeSnapshot { return localmesh.NodeSnapshot{} })
	r.inventoryFailed(f.creds[p533].Device, true, time.Now())
	// This entry point is called only after inbound mTLS identity verification.
	// Identifying/rate limiting unauthenticated addresses is not part of inventory.
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	done := make(chan struct{})
	go func() { defer close(done); r.sessionInventory(context.Background(), f.creds[p533].Device, left) }()
	_ = right.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	n, err := right.Read(b[:])
	if n != 0 || err == nil {
		t.Fatal("cooling inbound peer received application data")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cooling inbound peer allocated a session")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.peers) != 0 {
		t.Fatal("cooling inbound peer registered")
	}
}

// A service origin need not be a direct neighbour. Without an inventory session
// to that origin, its services stay unready even when Babel routes are healthy.
func TestDiscoverDialsRoutedOriginWithoutDirectLink(t *testing.T) {
	f := newFixture(t)
	c, _ := f.newCatalog(t, p533, "default", nil, nil)
	view := bridgeRouteView(t, time.Now(), p535)
	if len(view.Links) != 0 {
		t.Fatal("fixture unexpectedly has a direct link")
	}
	r, _ := NewRuntime(c, func() localmesh.NodeSnapshot { return view })
	dialed := make(chan netip.Addr, 1)
	r.dialPeer = func(ctx context.Context, self, peer netip.Addr) (net.Conn, error) {
		dialed <- peer
		return nil, errors.New("test stops at dial")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.discover(ctx, time.Now())
	select {
	case peer := <-dialed:
		if peer != f.creds[p535].Device.MeshAddress() {
			t.Fatalf("wrong origin: %s", peer)
		}
	case <-time.After(time.Second):
		t.Fatal("reachable multi-hop origin was not dialed")
	}
}
