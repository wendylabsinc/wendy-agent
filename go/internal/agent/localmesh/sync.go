package localmesh

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

const MaxControlMessage = 64 << 10

// Control messages use one bounded reliable stream per authenticated link;
// routing and payload packets remain independent QUIC datagrams.
type ControlMessage struct {
	Kind        string              `json:"kind"`
	Hello       *LinkHello          `json:"hello,omitempty"`
	Bundle      [][]byte            `json:"bundle,omitempty"`
	Manifest    *SignedManifest     `json:"manifest,omitempty"`
	Principal   string              `json:"principal,omitempty"`
	Certificate *CertificateMessage `json:"certificate,omitempty"`
}

func WriteControl(w io.Writer, m ControlMessage) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(data) > MaxControlMessage {
		return errors.New("oversized wendy net control message")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if err = writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, data)
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

func ReadControl(r io.Reader) (ControlMessage, error) {
	var m ControlMessage
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return m, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > MaxControlMessage {
		return m, errors.New("invalid control message length")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.Kind != "certificate" && m.Certificate != nil {
		return m, errors.New("unexpected certificate message")
	}
	switch m.Kind {
	case "certificate":
		if m.Certificate == nil || m.Hello != nil || m.Manifest != nil || len(m.Bundle) != 0 || m.Principal != "" {
			return m, errors.New("invalid certificate envelope")
		}
		if err := m.Certificate.validate(); err != nil {
			return m, err
		}
	case "hello":
		if m.Hello == nil || len(m.Bundle) != 0 || m.Manifest != nil || m.Principal != "" {
			return m, errors.New("invalid hello message")
		}
	case "bundle":
		if len(m.Bundle) == 0 || len(m.Bundle) > 16 || m.Manifest != nil || m.Principal != "" {
			return m, errors.New("invalid bundle message")
		}
	case "manifest":
		if m.Manifest == nil || len(m.Bundle) != 0 || m.Principal != "" {
			return m, errors.New("invalid manifest message")
		}
	case "identity-request":
		// Only a full device principal addresses an identity: no hash, no number.
		if _, err := meshid.DeviceFromPrincipal(m.Principal); err != nil {
			return m, errors.New("invalid identity request")
		}
		if m.Manifest != nil || len(m.Bundle) != 0 {
			return m, errors.New("invalid identity request")
		}
	default:
		return m, errors.New("unknown wendy net control message")
	}
	if m.Kind != "hello" && m.Hello != nil {
		return m, errors.New("unexpected hello payload")
	}
	return m, nil
}

// Synchronizer is per-link, single-caller, and does not own networking. Changed
// records are broadcast immediately by its caller; periodic Reconcile repairs
// loss/reconnect. Bounded chain descriptions replace unsolicited bundles;
// certificate bytes are fetched individually only when missing. A bounded
// number of manifests wait for independently verified identity completion.
type Synchronizer struct {
	directory    *Directory
	cache        *IdentityCache
	sent         map[string]bool
	pending      map[string]SignedManifest
	certificates *CertificateExchange
	// A repair pass resumes after the last admitted principal, rather than
	// repeatedly filling a bounded neighbour queue with its sorted prefix.
	reconcileAfter     string
	reconcileRemaining int
}

func NewSynchronizer(d *Directory, c *IdentityCache) *Synchronizer {
	return &Synchronizer{directory: d, cache: c, sent: map[string]bool{}, pending: map[string]SignedManifest{}, certificates: NewCertificateExchange(c, d.tenant)}
}

// SeedKnownBundles uses only the sender's per-peer record of completed writes.
// It does not trust the hint for authentication: the receiving directory still
// verifies each signed manifest against its own validated identity cache.
func (s *Synchronizer) SeedKnownBundles(fingerprints []string) {
	for _, fp := range fingerprints {
		if len(fp) == 64 {
			s.sent[fp] = true
		}
	}
}

func (s *Synchronizer) Record(w SignedManifest, now time.Time) []ControlMessage {
	messages, _ := s.recordBounded(w, now, 2)
	return messages
}

// The bundle hint is consumed only if the complete record fits. A caller must
// reserve this many queue slots before sending the returned messages.
func (s *Synchronizer) recordBounded(w SignedManifest, now time.Time, slots int) ([]ControlMessage, bool) {
	chain, ok := s.directory.CurrentRecordChain(w, now)
	if !ok {
		return nil, true
	}
	fp := Fingerprint(chain)
	bundleNeeded := !s.sent[fp]
	needed := 1
	if bundleNeeded {
		needed++
	}
	if needed > slots {
		return nil, false
	}
	var out []ControlMessage
	if bundleNeeded {
		if len(s.sent) >= 1024 {
			s.sent = map[string]bool{}
		}
		s.sent[fp] = true
		out = append(out, ControlMessage{Kind: "certificate", Certificate: DescribeCertificates(w.Principal, chain)})
	}
	w.Body = append(json.RawMessage(nil), w.Body...)
	w.Signature = append([]byte(nil), w.Signature...)
	return append(out, ControlMessage{Kind: "manifest", Manifest: &w}), true
}

func (s *Synchronizer) Reconcile(now time.Time) []ControlMessage {
	var out []ControlMessage
	for _, w := range s.directory.Records(now) {
		out = append(out, s.Record(w, now)...)
	}
	return out
}

// ReconcileBounded starts a repair pass using only the caller's free queue
// slots. ContinueReconcile drains the rest without restarting or resending the
// already admitted prefix. The normal repair interval may start a new pass;
// keeping the cursor ensures progress even if a slow link has not finished.
func (s *Synchronizer) ReconcileBounded(now time.Time, slots int) []ControlMessage {
	records := s.directory.Records(now)
	s.reconcileRemaining = len(records)
	return s.reconcileBatch(records, now, slots)
}

func (s *Synchronizer) ContinueReconcile(now time.Time, slots int) []ControlMessage {
	if s.reconcileRemaining == 0 || slots <= 0 {
		return nil
	}
	return s.reconcileBatch(s.directory.Records(now), now, slots)
}

func (s *Synchronizer) reconcileBatch(records []SignedManifest, now time.Time, slots int) []ControlMessage {
	s.reconcileRemaining = min(s.reconcileRemaining, len(records))
	if s.reconcileRemaining == 0 || slots <= 0 {
		return nil
	}
	start := sort.Search(len(records), func(i int) bool { return records[i].Principal > s.reconcileAfter })
	var out []ControlMessage
	for i := 0; s.reconcileRemaining > 0 && slots > 0; i++ {
		w := records[(start+i)%len(records)]
		messages, fits := s.recordBounded(w, now, slots)
		if !fits {
			break
		}
		out = append(out, messages...)
		slots -= len(messages)
		s.reconcileRemaining--
		s.reconcileAfter = w.Principal
	}
	return out
}

// Receive returns replies plus independently verified changed records to relay.
// Invalid signatures/identities are fatal to this link, not admitted as hints.
func (s *Synchronizer) Receive(m ControlMessage, now time.Time) (replies []ControlMessage, changed []SignedManifest, err error) {
	switch m.Kind {
	case "certificate":
		if m.Certificate == nil {
			return nil, nil, errors.New("missing certificate message")
		}
		replies, completed, err := s.certificates.Receive(*m.Certificate, now)
		return s.certificateResult(replies, completed, err, now)
	case "identity-request":
		chain, _, ok := s.cache.Get(m.Principal, now)
		if ok {
			replies = append(replies, ControlMessage{Kind: "certificate", Certificate: DescribeCertificates(m.Principal, chain)})
		}
	case "bundle":
		principal, e := s.cache.Put(m.Bundle, now)
		if e != nil {
			return nil, nil, e
		}
		if w, ok := s.pending[principal]; ok {
			delete(s.pending, principal)
			return s.Receive(ControlMessage{Kind: "manifest", Manifest: &w}, now)
		}
	case "manifest":
		if m.Manifest == nil {
			return nil, nil, errors.New("missing manifest")
		}
		ok, e := s.directory.Accept(*m.Manifest, now)
		// Reordering across relays and leases expiring in transit are normal,
		// not grounds to disconnect an authenticated neighbour.
		if errors.Is(e, ErrStaleManifest) || errors.Is(e, ErrExpiredManifest) {
			return nil, nil, nil
		}
		if errors.Is(e, ErrIdentityNeeded) {
			// Do not disconnect a valid neighbour if many hints are stale after
			// a receiver restart. The request repairs its identity cache, and
			// the periodic reconciliation repeats any manifest we cannot hold.
			if _, exists := s.pending[m.Manifest.Principal]; exists || len(s.pending) < 128 {
				s.pending[m.Manifest.Principal] = *m.Manifest
			}
			return []ControlMessage{{Kind: "identity-request", Principal: m.Manifest.Principal}}, nil, nil
		}
		if e != nil {
			return nil, nil, e
		}
		if ok {
			changed = append(changed, *m.Manifest)
		}
	default:
		return nil, nil, errors.New("unknown directory message")
	}
	return replies, changed, nil
}

func (s *Synchronizer) certificateResult(messages []*CertificateMessage, completed []string, err error, now time.Time) (replies []ControlMessage, changed []SignedManifest, result error) {
	if err != nil {
		return nil, nil, err
	}
	for _, m := range messages {
		replies = append(replies, ControlMessage{Kind: "certificate", Certificate: m})
	}
	for _, principal := range completed {
		if w, ok := s.pending[principal]; ok {
			delete(s.pending, principal)
			out, admitted, err := s.Receive(ControlMessage{Kind: "manifest", Manifest: &w}, now)
			if err != nil {
				return nil, nil, err
			}
			replies = append(replies, out...)
			changed = append(changed, admitted...)
		}
	}
	return replies, changed, nil
}

func (s *Synchronizer) ContinueCertificates(now time.Time) ([]ControlMessage, []SignedManifest, error) {
	messages, completed, err := s.certificates.Poll(now)
	return s.certificateResult(messages, completed, err, now)
}

func (s *Synchronizer) Close() { s.certificates.Close() }
