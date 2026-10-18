package mtls

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"
)

const (
	peerPathCacheEntries = 256
	peerPathCacheBytes   = 8 << 20
	peerPathCacheAge     = time.Minute
)

// peerPathCache reuses only the cryptographic path proof for an exact chain.
// It belongs to one immutable roots/usage/floor verifier incarnation. Callers
// must still check current identity, time and revocation policy on every call.
// Neither a failed verification nor a device authorization is retained here.
type peerPathCache struct {
	mu         sync.Mutex
	entries    map[[sha256.Size]byte]*list.Element
	recency    list.List
	bytes      int
	maxEntries int
	maxBytes   int
	clock      func() time.Time // time.Now retains a monotonic component.
	highWater  time.Time        // Wall clock only, across all observed lookups.
}

type peerPathEntry struct {
	key                         [sha256.Size]byte
	chain                       [][]byte
	bytes                       int
	verifiedWall, verifiedClock time.Time
	verifiedEffective, expires  time.Time
}

func newPeerPathCache() *peerPathCache {
	return &peerPathCache{entries: make(map[[sha256.Size]byte]*list.Element), maxEntries: peerPathCacheEntries, maxBytes: peerPathCacheBytes, clock: time.Now}
}

func samePeerChain(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func peerChainKey(chain [][]byte, limit int) (key [sha256.Size]byte, size int, ok bool) {
	if len(chain) == 0 || len(chain) > 32 {
		return key, 0, false
	}
	for _, der := range chain {
		// Include framing in the retained-byte budget and avoid integer overflow.
		if len(der) > limit-size-8 {
			return key, 0, false
		}
		size += len(der) + 8
	}
	h := sha256.New()
	var length [8]byte
	for _, der := range chain {
		binary.BigEndian.PutUint64(length[:], uint64(len(der)))
		_, _ = h.Write(length[:])
		_, _ = h.Write(der)
	}
	h.Sum(key[:0])
	return key, size, true
}

func (c *peerPathCache) remove(element *list.Element) {
	entry := element.Value.(peerPathEntry)
	delete(c.entries, entry.key)
	c.recency.Remove(element)
	c.bytes -= entry.bytes
}

// observe includes calls rejected by per-call policy before path verification,
// so an expiry failure followed by a wall rollback cannot reuse an older proof.
func (c *peerPathCache) observe(now time.Time) {
	now = now.Round(0)
	c.mu.Lock()
	if now.After(c.highWater) {
		c.highWater = now
	}
	c.mu.Unlock()
}

// verify requires both clocks used by path verification to move forward. A
// rollback always repeats verification, even if it remains inside a leaf's
// validity window. The fixed monotonic age prevents a stalled wall clock from
// extending reuse. Effective time is separate because a NotBefore floor can
// change discontinuously when wall time catches up with provisioning time.
func (c *peerPathCache) verify(chain [][]byte, wall, effective time.Time, verify func([][]byte, time.Time) (time.Time, error)) (time.Time, error) {
	wall, effective = wall.Round(0), effective.Round(0)
	key, size, cacheable := peerChainKey(chain, c.maxBytes)
	cacheable = cacheable && c.maxEntries > 0
	started := c.clock()
	c.mu.Lock()
	if wall.After(c.highWater) {
		c.highWater = wall
	}
	if element := c.entries[key]; cacheable && element != nil {
		entry := element.Value.(peerPathEntry)
		age := started.Sub(entry.verifiedClock)
		if !wall.Before(c.highWater) && !wall.Before(entry.verifiedWall) && !effective.Before(entry.verifiedEffective) &&
			wall.Before(entry.expires) && effective.Before(entry.expires) && age >= 0 && age < peerPathCacheAge && samePeerChain(chain, entry.chain) {
			c.recency.MoveToFront(element)
			c.mu.Unlock()
			return entry.expires, nil
		}
		c.remove(element)
	}
	c.mu.Unlock()
	if !cacheable {
		return verify(chain, wall)
	}
	// Verification and the stored proof use the same owned bytes. A caller's
	// later reuse of its input buffers cannot mutate an accepted cache entry.
	copyChain := make([][]byte, len(chain))
	for i, der := range chain {
		copyChain[i] = bytes.Clone(der)
	}
	expires, err := verify(copyChain, wall)
	if err != nil {
		return time.Time{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	age := c.clock().Sub(started)
	if wall.Before(c.highWater) || !wall.Before(expires) || !effective.Before(expires) || age < 0 || age >= peerPathCacheAge {
		return expires, nil
	}
	if previous := c.entries[key]; previous != nil {
		c.remove(previous)
	}
	for c.recency.Len() >= c.maxEntries || c.bytes+size > c.maxBytes {
		c.remove(c.recency.Back())
	}
	entry := peerPathEntry{key: key, chain: copyChain, bytes: size, verifiedWall: wall, verifiedClock: started, verifiedEffective: effective, expires: expires}
	c.entries[key] = c.recency.PushFront(entry)
	c.bytes += size
	return expires, nil
}
