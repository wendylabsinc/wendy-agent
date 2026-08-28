package tlscache

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/secretstore"
)

// memStore is an in-memory secretstore.Store recording deletes.
type memStore struct {
	mu      sync.Mutex
	m       map[string][]byte
	deletes int
}

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Get(key string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[key]
}
func (s *memStore) Put(key string, blob []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = blob
	return nil
}
func (s *memStore) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	s.deletes++
}

// startTLSServer runs a minimal TLS 1.3 server issuing session tickets;
// each accepted conn handshakes, reports DidResume on ch, writes one byte.
func startTLSServer(t *testing.T) (addr string, ch <-chan bool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "tlscache-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS13,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	resumed := make(chan bool, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tc := tls.Server(c, cfg)
				if err := tc.Handshake(); err != nil {
					return
				}
				resumed <- tc.ConnectionState().DidResume
				tc.Write([]byte{1}) // flushes the session ticket to the client
				buf := make([]byte, 1)
				tc.Read(buf) // wait for client close so the ticket is processed
			}(c)
		}
	}()
	return ln.Addr().String(), resumed
}

func dialWithCache(t *testing.T, addr string, cache *Cache) bool {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true,
		ClientSessionCache: cache,
		MinVersion:         tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil { // ensures NewSessionTicket has arrived
		t.Fatalf("read: %v", err)
	}
	return conn.ConnectionState().DidResume
}

func TestCacheRoundTripAcrossInstances(t *testing.T) {
	addr, srvResumed := startTLSServer(t)
	store := newMemStore()
	leafDER := []byte("client-leaf-der") // identity input only; any bytes work here

	c1 := newCache("cache-test", leafDER, store)
	if resumed := dialWithCache(t, addr, c1); resumed {
		t.Fatal("first connection unexpectedly resumed")
	}
	<-srvResumed
	c1.Flush() // wait for the async persist

	// A separate Cache instance over the same store simulates a new CLI process.
	c2 := newCache("cache-test", leafDER, store)
	if resumed := dialWithCache(t, addr, c2); !resumed {
		t.Fatal("second connection did not resume from persisted session")
	}
	if got := <-srvResumed; !got {
		t.Fatal("server did not observe resumption")
	}
}

func TestCacheKeyedByClientCert(t *testing.T) {
	addr, srvResumed := startTLSServer(t)
	store := newMemStore()

	c1 := newCache("cache-test", []byte("cert-A"), store)
	dialWithCache(t, addr, c1)
	<-srvResumed
	c1.Flush()

	// Same target, different client cert → different store key → no resumption.
	c2 := newCache("cache-test", []byte("cert-B"), store)
	if resumed := dialWithCache(t, addr, c2); resumed {
		t.Fatal("session resumed across different client certs")
	}
	<-srvResumed
}

func TestCacheCorruptBlobIsMissAndDeleted(t *testing.T) {
	store := newMemStore()
	c := newCache("cache-test", []byte("cert"), store)
	store.Put(c.storeKey, []byte("WTS1garbage-not-a-session"))
	if _, ok := c.Get("ignored"); ok {
		t.Fatal("corrupt blob returned a session")
	}
	if store.deletes == 0 {
		t.Error("corrupt blob was not deleted")
	}
}

func TestCachePutNilEvicts(t *testing.T) {
	store := newMemStore()
	c := newCache("cache-test", []byte("cert"), store)
	store.Put(c.storeKey, []byte("whatever"))
	c.Put("ignored", nil)
	c.Flush()
	if store.Get(c.storeKey) != nil {
		t.Error("Put(nil) did not evict the stored session")
	}
}

// TestSetResumedSkipsOverwriteOnPut is the regression test for the ticket-
// chaining finding: without SetResumed(true), Put overwrites the stored
// ticket on every connection — including resumed ones — because Go's TLS 1.3
// server reissues a fresh ticket even on a resumed handshake. A client that
// kept doing that could chain tickets forever and never re-run the full
// ML-DSA verification the design's ≤7-day trust bound depends on. The fix:
// once SetResumed(true) is called (simulating the always-on VerifyConnection
// wrapper in grpcclient.newAgentTLSConfig observing DidResume), Put must keep
// the ticket from the last FULL handshake instead of overwriting it.
func TestSetResumedSkipsOverwriteOnPut(t *testing.T) {
	addr, srvResumed := startTLSServer(t)
	store := newMemStore()
	c := newCache("cache-test", []byte("client-leaf-der"), store)

	// First connection: full handshake, persists a ticket.
	if resumed := dialWithCache(t, addr, c); resumed {
		t.Fatal("first connection unexpectedly resumed")
	}
	<-srvResumed
	c.Flush()

	stored := store.Get(c.storeKey)
	if stored == nil {
		t.Fatal("first full handshake did not persist a session")
	}
	wantUnchanged := append([]byte(nil), stored...)

	// Simulate the always-on VerifyConnection wrapper having observed
	// DidResume on the connection that is about to call Put.
	c.SetResumed(true)

	// Second connection: the ticket persisted above is still valid, so this
	// resumes at the TLS layer, and the server (per Go's TLS 1.3 behavior)
	// issues a fresh ticket that crypto/tls delivers via a REAL Put call —
	// exercising the actual code path Put must guard, not a fabricated
	// ClientSessionState.
	if resumed := dialWithCache(t, addr, c); !resumed {
		t.Fatal("second connection did not resume — can't exercise SetResumed's effect on a resumed-connection Put")
	}
	<-srvResumed
	c.Flush()

	if got := store.Get(c.storeKey); !bytes.Equal(got, wantUnchanged) {
		t.Error("Put overwrote the full-handshake ticket after SetResumed(true); stored blob changed on a resumed connection")
	}
}

// TestCachePutNilEvictsEvenWhenMarkedResumed covers the other half of
// SetResumed's contract: eviction (Put(nil)) must always run regardless of
// the resumed flag — crypto/tls uses Put(nil) to drop sessions it has
// determined are broken, and skipping that would leave a bad ticket cached.
func TestCachePutNilEvictsEvenWhenMarkedResumed(t *testing.T) {
	store := newMemStore()
	c := newCache("cache-test", []byte("cert"), store)
	store.Put(c.storeKey, []byte("whatever"))
	c.SetResumed(true)
	c.Put("ignored", nil)
	c.Flush()
	if store.Get(c.storeKey) != nil {
		t.Error("Put(nil) did not evict the stored session even though SetResumed(true) had been called")
	}
}

// TestSetResumedFalseAllowsPutAfterLaterFullHandshake is the regression test
// for the sticky-flag finding: MarkResumed used to only ever set resumed
// true, so a grpc.ClientConn's shared *Cache — reused across the ClientConn's
// internal reconnect handshakes — would never persist a ticket from a LATER
// legitimate full handshake once any earlier handshake on that connection had
// resumed. The fix is SetResumed(bool): the always-on VerifyConnection
// wrapper now calls it on every handshake with cs.DidResume, so a later full
// handshake (DidResume=false) clears the flag and Put persists again.
//
// This drives the scenario with real dials plus one direct SetResumed(false)
// standing in for the wrapper's call on a later full handshake (there is no
// grpc.ClientConn here to force a real third TLS 1.3 dial to skip
// resumption on a still-valid ticket): dial 1 is a full handshake and
// persists; SetResumed(true) simulates the wrapper observing the resumed
// dial 2 would produce; the store entry is then evicted out-of-band (as if
// the ticket expired or the server rotated its STEK) so dial 3 is forced to
// be a full handshake; SetResumed(false) simulates the wrapper observing
// that dial 3's DidResume; and Put's subsequent persist of dial 3's fresh
// ticket must succeed. Verified to fail against the old sticky MarkResumed
// semantics (permanent regression-test doc note; see the residual-fix report
// for the negative-verification run).
func TestSetResumedFalseAllowsPutAfterLaterFullHandshake(t *testing.T) {
	addr, srvResumed := startTLSServer(t)
	store := newMemStore()
	c := newCache("cache-test", []byte("client-leaf-der"), store)

	// Dial 1: full handshake, persists a ticket.
	if resumed := dialWithCache(t, addr, c); resumed {
		t.Fatal("first connection unexpectedly resumed")
	}
	<-srvResumed
	c.Flush()
	if store.Get(c.storeKey) == nil {
		t.Fatal("first full handshake did not persist a session")
	}

	// Simulate the always-on VerifyConnection wrapper observing a resumed
	// handshake on this same Cache (as would happen on a grpc.ClientConn
	// internal reconnect that resumes).
	c.SetResumed(true)

	// The ticket persisted above is evicted out-of-band here (standing in for
	// expiry/STEK rotation) so the next dial is forced into a full handshake
	// without needing a second real server or clock manipulation.
	store.Delete(c.storeKey)
	c.wg.Wait() // no pending async work from the delete above; keeps ordering explicit

	// Simulate the wrapper observing dial 3's full handshake — this is the
	// exact call that must reset the sticky flag from the resumed dial 2
	// simulated above.
	c.SetResumed(false)

	// Dial 3: the store entry is gone, so this must be a full handshake, and
	// its fresh ticket must persist — this is the case the old sticky
	// MarkResumed (set-only-true, never reset) got wrong: once any handshake
	// on this Cache had resumed, no later full handshake's ticket would ever
	// be saved again.
	if resumed := dialWithCache(t, addr, c); resumed {
		t.Fatal("third connection unexpectedly resumed — test setup did not force a full handshake")
	}
	<-srvResumed
	c.Flush()

	if store.Get(c.storeKey) == nil {
		t.Error("later full handshake's ticket was not persisted after an earlier resumed handshake on the same Cache — SetResumed did not reset")
	}
}

// TestKeychainPutCommandLineSizeForSessionTicket measures — and permanently
// guards — the real security(1) command-line size for a captured TLS session
// ticket blob going through the ACTUAL darwin keychain Put path (not a fake
// blob): it fakes secretstore.RunSecurity to capture the stdin the real
// keychain.Put builds, then drives a real handshake so the blob is a genuine
// encoded session ticket. This is the FINDING 1 measurement: the 4000-byte
// truncation guard in secretstore's keychain Put protects this path too, and
// this test proves the real ticket size stays comfortably under it.
func TestKeychainPutCommandLineSizeForSessionTicket(t *testing.T) {
	store := secretstore.NewKeychain(keychainService)
	if store == nil {
		t.Skip("no Keychain backend on this platform")
	}

	var captured string
	origRun := secretstore.RunSecurity
	secretstore.RunSecurity = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		// Put probes for a writable keychain before it writes (see
		// secretstore/keychain_probe_darwin.go); answer both probes so the
		// write this test measures is actually reached.
		switch args[0] {
		case "default-keychain":
			return []byte("    \"/Users/tester/Library/Keychains/login.keychain-db\"\n"), nil
		case "show-keychain-info":
			return []byte("no-timeout\n"), nil
		}
		captured = stdin
		return nil, nil
	}
	t.Cleanup(func() { secretstore.RunSecurity = origRun })

	addr, srvResumed := startTLSServer(t)
	c := newCache("cache-test", []byte("client-leaf-der"), store)
	if resumed := dialWithCache(t, addr, c); resumed {
		t.Fatal("first connection unexpectedly resumed")
	}
	<-srvResumed
	c.Flush()

	if captured == "" {
		t.Fatal("Put was never invoked through the faked security runner")
	}
	t.Logf("keychain Put command line for a session-ticket blob = %d bytes", len(captured))
	if len(captured) >= 4000 {
		t.Errorf("session-ticket keychain command line = %d bytes, at/over the 4000-byte truncation guard in secretstore's keychain Put", len(captured))
	}
}

func TestForTargetOffReturnsNil(t *testing.T) {
	t.Setenv("WENDY_TLS_SESSION_STORE", "off")
	if c := ForTarget("host:50052", []byte("cert")); c != nil {
		t.Errorf("ForTarget with store=off = %v, want nil", c)
	}
}
