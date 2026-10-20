package localmesh

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

const (
	MaxManifestBytes = 32768
	MaxLease         = 2 * time.Minute
	manifestDomain   = "wendy-local-mesh/manifest/2\x00"
)

// Manifest is a device's own advertisement. It names the device by its UUID -
// the same 16 bytes as the last ten bytes of its mesh address - and carries no
// organization number, asset number, or origin proof: admission to the mesh is
// the authenticated link, not this document.
type Manifest struct {
	Version int       `json:"version"`
	Device  uuid.UUID `json:"device"`
	// Revision is a persistently increasing origin counter, including restart.
	Revision        uint64 `json:"revision"`
	Issued, Expires int64
	Name            string `json:"name"`
	AgentPort       uint16 `json:"agentPort"`
	Withdraw        bool   `json:"withdraw,omitempty"`
}

// SignedManifest is a manifest plus the full device principal of the identity
// that signed it. The principal is the lookup key; the manifest body carries the
// UUID, and Accept requires the two to agree.
type SignedManifest struct {
	Principal string          `json:"principal"`
	Body      json.RawMessage `json:"body"`
	Signature []byte          `json:"signature"`
}

var ErrExpiredManifest = errors.New("expired wendy net manifest")
var ErrStaleManifest = errors.New("stale wendy net manifest")

func (m Manifest) validate(now time.Time) error {
	issued, expires := time.UnixMilli(m.Issued), time.UnixMilli(m.Expires)
	if !expires.After(now) {
		return ErrExpiredManifest
	}
	if m.Version != 1 || m.Revision == 0 ||
		issued.After(now.Add(5*time.Second)) || !expires.After(issued) || expires.Sub(issued) > MaxLease ||
		len(m.Name) > 255 || (!m.Withdraw && m.AgentPort == 0) {
		return errors.New("invalid or expired wendy net manifest")
	}
	return nil
}

func signingInput(key crypto.PublicKey, body []byte) ([]byte, crypto.SignerOpts) {
	input := append([]byte(manifestDomain), body...)
	if _, ok := key.(ed25519.PublicKey); ok {
		return input, crypto.Hash(0)
	}
	h := sha256.Sum256(input)
	return h[:], crypto.SHA256
}

// SignManifest signs an advertisement with the provisioned device identity and
// records that identity's principal. The manifest's device UUID must be the
// signing leaf's own; an advertisement for another device is refused here rather
// than discovered later by a receiver.
func SignManifest(m Manifest, chain [][]byte, signer crypto.Signer, now time.Time) (SignedManifest, error) {
	var out SignedManifest
	if err := m.validate(now); err != nil {
		return out, err
	}
	if signer == nil || len(chain) == 0 {
		return out, errors.New("missing manifest signer")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return out, err
	}
	device, err := meshid.DeviceFromCert(leaf)
	if err != nil {
		return out, err
	}
	if device.UUID() != m.Device {
		return out, errors.New("manifest identity mismatch")
	}
	out.Principal = device.Principal()
	body, err := json.Marshal(m)
	if err != nil {
		return out, err
	}
	if len(body) > MaxManifestBytes {
		return out, errors.New("oversized manifest")
	}
	input, opts := signingInput(signer.Public(), body)
	sig, err := signer.Sign(rand.Reader, input, opts)
	if err != nil {
		return out, err
	}
	out.Body, out.Signature = body, sig
	return out, nil
}

// ErrIdentityNeeded lets the synchronizer request an independently validated
// bundle from its neighbour. Unverified announcements are not retained here.
var ErrIdentityNeeded = errors.New("wendy net identity bundle needed")

type directoryEntry struct {
	manifest Manifest
	wire     SignedManifest
	hash     string
	// Tombstones and superseded versions live for a full maximum lease from
	// admission, so removing a short-lived update cannot resurrect an older one.
	retainUntil time.Time
}

type Directory struct {
	mu      sync.Mutex
	tenant  meshid.Tenant
	limit   int
	cache   *IdentityCache
	entries map[string]directoryEntry
	history map[string]DirectoryReceipt
	persist func([]DirectoryReceipt) error
}

// DirectoryReceipt is durable replay protection, not cached presence.
type DirectoryReceipt struct {
	Device   string    `json:"device"` // full device principal
	Revision uint64    `json:"revision"`
	Hash     string    `json:"hash"`
	Until    time.Time `json:"until"`
}

func (d *Directory) SetPersistence(receipts []DirectoryReceipt, persist func([]DirectoryReceipt) error, now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(receipts) > d.limit || persist == nil {
		return errors.New("invalid directory persistence")
	}
	history := map[string]DirectoryReceipt{}
	for _, r := range receipts {
		if len(r.Device) > maxPrincipalBytes || r.Revision == 0 || len(r.Hash) != 64 {
			return errors.New("invalid directory receipt")
		}
		device, err := meshid.DeviceFromPrincipal(r.Device)
		if err != nil || device.Tenant() != d.tenant {
			return errors.New("invalid directory receipt")
		}
		if r.Until.After(now) {
			history[r.Device] = r
		}
	}
	d.history = history
	d.persist = persist
	return nil
}

// NewDirectory scopes a directory to one tenant: the tenant half of every
// principal it will accept.
func NewDirectory(tenant meshid.Tenant, limit int, cache *IdentityCache) (*Directory, error) {
	if tenant == (meshid.Tenant{}) || limit < 1 || cache == nil {
		return nil, errors.New("invalid directory configuration")
	}
	return &Directory{tenant: tenant, limit: limit, cache: cache, entries: map[string]directoryEntry{}}, nil
}

// Accept validates the advertising device's own verified identity, never the
// relaying neighbour: a manifest is admitted because its signer is the device
// named in it, and that device was admitted by the authenticated link. Return
// true only for a new version: duplicate delivery never renews its signed
// absolute expiry.
func (d *Directory) Accept(w SignedManifest, now time.Time) (bool, error) {
	if len(w.Body) > MaxManifestBytes || len(w.Signature) > 1024 || w.Principal == "" || len(w.Principal) > maxPrincipalBytes {
		return false, errors.New("oversized manifest envelope")
	}
	device, err := meshid.DeviceFromPrincipal(w.Principal)
	if err != nil {
		return false, err
	}
	if device.Tenant() != d.tenant {
		return false, errors.New("manifest belongs to another tenant")
	}
	var m Manifest
	if err := json.Unmarshal(w.Body, &m); err != nil {
		return false, err
	}
	if err = m.validate(now); err != nil {
		return false, err
	}
	chain, id, ok := d.cache.Get(w.Principal, now)
	if !ok {
		return false, ErrIdentityNeeded
	}
	if id.Device.Principal() != device.Principal() {
		return false, errors.New("manifest identity mismatch")
	}
	if m.Device != device.UUID() {
		return false, errors.New("manifest identity mismatch")
	}
	if time.UnixMilli(m.Expires).After(id.NotAfter) {
		return false, errors.New("manifest outlives certificate")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return false, err
	}
	input, _ := signingInput(leaf.PublicKey, w.Body)
	valid := false
	switch key := leaf.PublicKey.(type) {
	case ed25519.PublicKey:
		valid = ed25519.Verify(key, input, w.Signature)
	case *ecdsa.PublicKey:
		valid = ecdsa.VerifyASN1(key, input, w.Signature)
	case *rsa.PublicKey:
		valid = rsa.VerifyPKCS1v15(key, crypto.SHA256, input, w.Signature) == nil
	}
	if !valid {
		return false, errors.New("invalid manifest signature")
	}
	hash := manifestHash(w.Body)
	d.mu.Lock()
	defer d.mu.Unlock()
	for principal, e := range d.entries {
		if !e.retainUntil.After(now) {
			delete(d.entries, principal)
		}
	}
	if d.history == nil {
		d.history = map[string]DirectoryReceipt{}
	}
	if receipt, ok := d.history[device.Principal()]; ok && receipt.Until.After(now) {
		if m.Revision < receipt.Revision {
			return false, ErrStaleManifest
		}
		if m.Revision == receipt.Revision && hash != receipt.Hash {
			return false, errors.New("conflicting persisted manifest revision")
		}
	}
	if old, exists := d.entries[device.Principal()]; exists {
		if m.Revision < old.manifest.Revision {
			return false, ErrStaleManifest
		}
		if m.Revision == old.manifest.Revision {
			if hash != old.hash {
				return false, errors.New("conflicting manifest revision")
			}
			return false, nil
		}
	} else if len(d.entries) >= d.limit {
		return false, errors.New("directory capacity reached")
	}
	w.Body = append(json.RawMessage(nil), w.Body...)
	w.Signature = append([]byte(nil), w.Signature...)
	next := make(map[string]DirectoryReceipt, len(d.history)+1)
	for principal, r := range d.history {
		if r.Until.After(now) {
			next[principal] = r
		}
	}
	if _, ok := next[device.Principal()]; !ok && len(next) >= d.limit {
		return false, errors.New("directory receipt capacity reached")
	}
	next[device.Principal()] = DirectoryReceipt{Device: device.Principal(), Revision: m.Revision, Hash: hash, Until: now.Add(MaxLease + 5*time.Second)}
	if d.persist != nil {
		all := make([]DirectoryReceipt, 0, len(next))
		for _, r := range next {
			all = append(all, r)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Device < all[j].Device })
		if err := d.persist(all); err != nil {
			return false, err
		}
	}
	d.history = next
	d.entries[device.Principal()] = directoryEntry{m, w, hash, now.Add(MaxLease + 5*time.Second)}
	return true, nil
}

func manifestHash(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// Snapshot returns current origin records. Route reachability is a separate
// filter at the consumer, never inferred from having a directory entry.
func (d *Directory) Snapshot(now time.Time) []Manifest {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []Manifest{}
	for _, e := range d.entries {
		m := e.manifest
		if m.Withdraw || !time.UnixMilli(m.Expires).After(now) {
			continue
		}
		if _, _, ok := d.cache.Get(e.wire.Principal, now); !ok {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].Device[:], out[j].Device[:]) < 0 })
	return out
}

// Records includes signed withdrawals for anti-entropy. Expired records are not
// retransmitted; compact revision tombstones still prevent local resurrection.
func (d *Directory) Records(now time.Time) []SignedManifest {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []SignedManifest{}
	for _, e := range d.entries {
		if !time.UnixMilli(e.manifest.Expires).After(now) {
			continue
		}
		w := e.wire
		w.Body = append(json.RawMessage(nil), w.Body...)
		w.Signature = append([]byte(nil), w.Signature...)
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Principal != out[j].Principal {
			return out[i].Principal < out[j].Principal
		}
		return string(out[i].Body) < string(out[j].Body)
	})
	return out
}
