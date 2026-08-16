package tlscache

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"sync/atomic"

	"github.com/wendylabsinc/wendy/go/internal/shared/secretstore"
)

// blobMagic versions the on-disk/on-keychain blob layout:
// "WTS1" | uint32 BE ticket length | ticket | SessionState.Bytes().
const blobMagic = "WTS1"

// Cache implements tls.ClientSessionCache for a single (target address, client
// certificate) pair, persisting the most recent session via a secretstore.Store.
//
// The store key binds the client leaf certificate on purpose: a session ticket
// embeds the client identity verified at the original handshake, so a ticket
// obtained with one org's cert must never be offered when dialing with
// another's. Go's own sessionKey (the remote address) is ignored.
type Cache struct {
	storeKey string
	store    secretstore.Store
	wg       sync.WaitGroup

	// resumed reflects the MOST RECENT handshake performed on the connection
	// this Cache is bound to — set via SetResumed on every handshake, not just
	// resumed ones. A single *tls.Config (and its ClientSessionCache) is
	// reused by a grpc.ClientConn across internal reconnects, so "resumed"
	// must track the latest handshake rather than sticking true forever: a
	// later legitimate FULL handshake on that same connection has to be able
	// to persist its fresh ticket again. Go's TLS 1.3 server reissues a fresh
	// ticket on EVERY connection, including resumed ones (see the design
	// spec's "Self-refresh" note), so without this flag Put would overwrite
	// the stored full-handshake ticket with a resumed-connection ticket on
	// every call. That would let tickets chain indefinitely across resumed
	// connections and defeat the periodic full ML-DSA re-verification the
	// 7-day ticket lifetime is meant to force (see session_ticket.go's
	// trust-model comment on the agent side). Put(nil) — session eviction —
	// ignores this flag: crypto/tls uses it to drop broken/failed sessions and
	// that must always take effect.
	resumed atomic.Bool
}

// SetResumed records whether the connection this Cache is bound to resumed a
// previous session (true) or performed a full handshake (false) on its most
// recent handshake. When true, the next Put call with a non-nil session (the
// fresh ticket Go issues even on a resumed connection) is a no-op, keeping
// whichever ticket was stored by the last FULL handshake instead. When false,
// Put persists normally — this is what lets a later full handshake on a
// ClientConn that previously resumed still refresh the stored ticket.
//
// Callers must invoke this from a VerifyConnection hook on EVERY handshake
// (not only when resumed is true), which crypto/tls runs synchronously inside
// the handshake — strictly before it processes the server's post-handshake
// NewSessionTicket message and calls Put (ticket processing happens lazily on
// a later Read, after Handshake/VerifyConnection has already returned). gRPC
// dials/handshakes transports on a ClientConn sequentially, so that per-
// handshake VerifyConnection-before-Put ordering holds even though the Cache
// is shared and reused across reconnects — there is no race between marking
// and persisting for a single connection.
func (c *Cache) SetResumed(resumed bool) { c.resumed.Store(resumed) }

// ForTarget returns a Cache bound to the target address and client leaf cert
// (DER), or nil when session caching is disabled (WENDY_TLS_SESSION_STORE=off
// or no usable backend). Callers must skip the tls.Config wiring on nil — a
// nil *Cache inside a non-nil interface value would panic in crypto/tls.
func ForTarget(address string, clientLeafDER []byte) *Cache {
	store := newDefaultStore()
	if store == nil {
		return nil
	}
	return newCache(address, clientLeafDER, store)
}

func newCache(address string, clientLeafDER []byte, store secretstore.Store) *Cache {
	certSum := sha256.Sum256(clientLeafDER)
	sum := sha256.Sum256(append([]byte(address+"|"), certSum[:]...))
	return &Cache{storeKey: hex.EncodeToString(sum[:]), store: store}
}

// Get implements tls.ClientSessionCache. Any decode failure evicts the entry
// and reports a miss; the caller then performs a full handshake.
func (c *Cache) Get(string) (*tls.ClientSessionState, bool) {
	blob := c.store.Get(c.storeKey)
	if blob == nil {
		return nil, false
	}
	ticket, stateBytes, ok := decodeSessionBlob(blob)
	if !ok {
		c.store.Delete(c.storeKey)
		return nil, false
	}
	state, err := tls.ParseSessionState(stateBytes)
	if err != nil {
		c.store.Delete(c.storeKey)
		return nil, false
	}
	cs, err := tls.NewResumptionState(ticket, state)
	if err != nil {
		c.store.Delete(c.storeKey)
		return nil, false
	}
	return cs, true
}

// Put implements tls.ClientSessionCache. crypto/tls calls it from the
// connection's record-processing path, so persistence (a file write, or a
// `security` subprocess under WENDY_TLS_SESSION_STORE=keychain) happens on a
// background goroutine; a ticket lost to process exit
// just means a full handshake next time. Put(nil) evicts (crypto/tls uses
// that on certain handshake failures) and always runs, even when the most
// recent SetResumed call reported true — a broken session must still be
// dropped regardless of how the prior handshake went. A non-nil session on an
// already-resumed connection is dropped instead of stored; see SetResumed's
// doc.
func (c *Cache) Put(_ string, cs *tls.ClientSessionState) {
	if cs == nil {
		c.async(func() { c.store.Delete(c.storeKey) })
		return
	}
	if c.resumed.Load() {
		return
	}
	ticket, state, err := cs.ResumptionState()
	if err != nil || state == nil {
		return
	}
	stateBytes, err := state.Bytes()
	if err != nil {
		return
	}
	blob := encodeSessionBlob(ticket, stateBytes)
	// The cache treats a lost ticket as a dropped optimization — a full
	// handshake next time — so a persist failure is silently ignored here.
	c.async(func() { _ = c.store.Put(c.storeKey, blob) })
}

// Flush blocks until all pending async persists complete. Tests rely on it;
// callers may use it before exit to make best-effort persistence certain.
func (c *Cache) Flush() { c.wg.Wait() }

func (c *Cache) async(fn func()) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		fn()
	}()
}

func encodeSessionBlob(ticket, state []byte) []byte {
	blob := make([]byte, 0, len(blobMagic)+4+len(ticket)+len(state))
	blob = append(blob, blobMagic...)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ticket)))
	blob = append(blob, l[:]...)
	blob = append(blob, ticket...)
	blob = append(blob, state...)
	return blob
}

func decodeSessionBlob(blob []byte) (ticket, state []byte, ok bool) {
	header := len(blobMagic) + 4
	if len(blob) < header || string(blob[:len(blobMagic)]) != blobMagic {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(blob[len(blobMagic):header])
	if uint32(len(blob)-header) < n {
		return nil, nil, false
	}
	return blob[header : header+int(n)], blob[header+int(n):], true
}
