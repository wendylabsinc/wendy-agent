package localmesh

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

const (
	maxCertificateBytes   = 64 << 10
	maxCertificateObjects = 4096
	maxCertificatePending = 128
	maxCertificatePool    = 8 << 20
	certificateChunkBytes = 1024
	certificateFetchTTL   = 30 * time.Second
)

type certificateObject struct {
	data          []byte
	expires, used time.Time
}

type certificateFetch struct {
	owner   uint64
	expires time.Time
	data    []byte
	total   int
}

// CertificatePool shares exact public certificate bytes between identity
// caches. Neither a hash match nor presence here authorizes an identity:
// IdentityCache.Put must verify every assembled chain under current trust.
// Completed objects and unfinished transfers together retain at most 8 MiB.
type CertificatePool struct {
	mu      sync.Mutex
	objects map[string]certificateObject
	pending map[string]*certificateFetch
	owner   uint64
	bytes   int
}

func NewCertificatePool() *CertificatePool {
	return &CertificatePool{objects: make(map[string]certificateObject), pending: make(map[string]*certificateFetch)}
}

func certificateHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validCertificateHash(hash string) bool {
	if len(hash) != 2*sha256.Size {
		return false
	}
	decoded, err := hex.DecodeString(hash)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == hash
}

func (p *CertificatePool) prune(now time.Time) {
	for hash, obj := range p.objects {
		if !obj.expires.After(now) {
			p.bytes -= len(obj.data)
			delete(p.objects, hash)
		}
	}
	for hash, fetch := range p.pending {
		if !fetch.expires.After(now) {
			p.bytes -= fetch.total
			delete(p.pending, hash)
		}
	}
}

func (p *CertificatePool) room(bytes int, object bool) bool {
	for p.bytes+bytes > maxCertificatePool || object && len(p.objects) >= maxCertificateObjects {
		var oldest string
		var used time.Time
		for hash, obj := range p.objects {
			if oldest == "" || obj.used.Before(used) || obj.used.Equal(used) && hash < oldest {
				oldest, used = hash, obj.used
			}
		}
		if oldest == "" {
			return false
		}
		p.bytes -= len(p.objects[oldest].data)
		delete(p.objects, oldest)
	}
	return true
}

func (p *CertificatePool) put(data []byte, expires, now time.Time) {
	if len(data) == 0 || len(data) > maxCertificateBytes || !expires.After(now) {
		return
	}
	hash := certificateHash(data)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(now)
	if fetch := p.pending[hash]; fetch != nil {
		p.bytes -= fetch.total
		delete(p.pending, hash)
	}
	if old, ok := p.objects[hash]; ok {
		old.expires = maxTime(old.expires, expires)
		old.used = now
		p.objects[hash] = old
		return
	}
	if !p.room(len(data), true) {
		return
	}
	p.objects[hash] = certificateObject{data: append([]byte(nil), data...), expires: expires, used: now}
	p.bytes += len(data)
}

func maxTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

func (p *CertificatePool) get(hash string, now time.Time) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(now)
	obj, ok := p.objects[hash]
	if !ok {
		return nil
	}
	obj.used = now
	p.objects[hash] = obj
	return append([]byte(nil), obj.data...)
}

func (p *CertificatePool) newOwner() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.owner++
	return p.owner
}

// claim coalesces a missing element across all neighbours and both directory
// and service identity caches sharing this pool. Only its first supplier gets
// a request. A disconnected supplier releases its claim immediately.
func (p *CertificatePool) claim(hash string, owner uint64, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(now)
	if _, found := p.objects[hash]; found || p.pending[hash] != nil || len(p.pending) >= maxCertificatePending {
		return false
	}
	p.pending[hash] = &certificateFetch{owner: owner, expires: now.Add(certificateFetchTTL)}
	return true
}

func (p *CertificatePool) release(owner uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for hash, fetch := range p.pending {
		if fetch.owner == owner {
			p.bytes -= fetch.total
			delete(p.pending, hash)
		}
	}
}
