package tlscache

import (
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
)

// memStore is an in-memory sessionStore recording deletes.
type memStore struct {
	mu      sync.Mutex
	m       map[string][]byte
	deletes int
}

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) get(key string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[key]
}
func (s *memStore) put(key string, blob []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = blob
}
func (s *memStore) delete(key string) {
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
	store.put(c.storeKey, []byte("WTS1garbage-not-a-session"))
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
	store.put(c.storeKey, []byte("whatever"))
	c.Put("ignored", nil)
	c.Flush()
	if store.get(c.storeKey) != nil {
		t.Error("Put(nil) did not evict the stored session")
	}
}

func TestForTargetOffReturnsNil(t *testing.T) {
	t.Setenv("WENDY_TLS_SESSION_STORE", "off")
	if c := ForTarget("host:50052", []byte("cert")); c != nil {
		t.Errorf("ForTarget with store=off = %v, want nil", c)
	}
}
