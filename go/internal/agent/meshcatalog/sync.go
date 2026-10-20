package meshcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
)

const MaxMessageBytes = 64 << 10

const maxPendingRecords = 128
const maxPendingRecordBytes = 128 << 10

var errPendingIdentities = errors.New("too many pending wendy net service identities")

// Message is carried on a reliable, authenticated mesh control stream.
// Signed records retain their origin proof across relays. Missing certificate
// elements are requested after reordered announcements or cache misses.
type Message struct {
	Kind        string                        `json:"kind"`
	Record      *SignedRecord                 `json:"record,omitempty"`
	Bundle      [][]byte                      `json:"bundle,omitempty"`
	Principal   string                        `json:"principal,omitempty"`
	Certificate *localmesh.CertificateMessage `json:"certificate,omitempty"`
}

func (m Message) validate() error {
	if m.Kind != "certificate" && m.Certificate != nil {
		return errors.New("unexpected service certificate message")
	}
	switch m.Kind {
	case "certificate":
		if m.Certificate == nil || m.Record != nil || len(m.Bundle) != 0 || m.Principal != "" {
			return errors.New("invalid service certificate envelope")
		}
		return m.Certificate.Validate()
	case "record":
		if m.Record == nil || len(m.Bundle) != 0 || m.Principal != "" {
			return errors.New("invalid service record message")
		}
	case "bundle":
		if m.Record != nil || len(m.Bundle) == 0 || len(m.Bundle) > 16 || m.Principal != "" {
			return errors.New("invalid service bundle message")
		}
	case "identity-request":
		if m.Record != nil || len(m.Bundle) != 0 ||
			m.Principal == "" || len(m.Principal) > 512 {
			return errors.New("invalid service identity request")
		}
	case "snapshot-done":
		if m.Record != nil || len(m.Bundle) != 0 || m.Principal != "" {
			return errors.New("invalid service snapshot completion")
		}
	default:
		return errors.New("unknown service message")
	}
	return nil
}

func WriteMessage(w io.Writer, m Message) error {
	if err := m.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > MaxMessageBytes {
		return errors.New("oversized service message")
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if err := writeFull(w, hdr[:]); err != nil {
		return err
	}
	return writeFull(w, data)
}

func writeFull(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func ReadMessage(r io.Reader) (Message, error) {
	var m Message
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return m, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxMessageBytes {
		return m, errors.New("invalid service message length")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return m, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, err
	}
	return m, m.validate()
}

// Synchronizer is owned by one peer link. Callers fan out changed signed
// records and periodically Reconcile to repair a partition or dropped frame.
type Synchronizer struct {
	catalog *Catalog
	cache   *localmesh.IdentityCache
	sent    map[string]bool
	// One authenticated neighbour control stream delivers records in order. Remember the
	// exact signed body last queued for each service so the periodic repair
	// pass sends only records that a dropped Broadcast never queued.
	sentRecords  map[Key]sentRecordStamp
	pending      map[string][]SignedRecord
	certificates *localmesh.CertificateExchange
}

type sentRecordStamp struct {
	generation uint64
	digest     [32]byte
}

func NewSynchronizer(c *Catalog, cache *localmesh.IdentityCache) *Synchronizer {
	return &Synchronizer{catalog: c, cache: cache, sent: map[string]bool{},
		sentRecords: map[Key]sentRecordStamp{}, pending: map[string][]SignedRecord{}, certificates: localmesh.NewCertificateExchange(cache, c.device.Tenant())}
}

func (s *Synchronizer) Record(w SignedRecord, now time.Time) []Message {
	var record Record
	if json.Unmarshal(w.Body, &record) != nil {
		return nil
	}
	stamp := sha256.Sum256(append([]byte(w.Fingerprint), w.Body...))
	if old, found := s.sentRecords[record.Key]; found &&
		(record.Generation < old.generation || record.Generation == old.generation && old.digest == stamp) {
		return nil
	}
	// Suppressing an already-queued record does not consume its identity or
	// transmit data. Check the stamp first; every record actually transmitted
	// still needs a current-trust lookup below. Catalog.Records independently
	// validates origins before reconciliation/projection.
	_, _, ok := s.cache.Get(record.Key.Device, now)
	if !ok {
		return nil
	}
	var out []Message
	if !s.sent[w.Fingerprint] {
		if len(s.sent) >= 1024 {
			s.sent = map[string]bool{}
		}
		s.sent[w.Fingerprint] = true
		out = append(out, Message{Kind: "certificate", Certificate: s.certificates.Describe(record.Key.Device, now)})
	}
	w = cloneWire(w)
	s.sentRecords[record.Key] = sentRecordStamp{record.Generation, stamp}
	return append(out, Message{Kind: "record", Record: &w})
}

func (s *Synchronizer) Reconcile(now time.Time) []Message {
	var out []Message
	live := make(map[Key]bool)
	for _, w := range s.catalog.Records(now) {
		var record Record
		if json.Unmarshal(w.Body, &record) == nil {
			live[record.Key] = true
		}
		out = append(out, s.Record(w, now)...)
	}
	for key := range s.sentRecords {
		if !live[key] {
			delete(s.sentRecords, key)
		}
	}
	return out
}

// snapshotRecords retains only stamps for keys still present in the bounded
// catalog. The runtime feeds this snapshot incrementally to its bounded writer.
func (s *Synchronizer) snapshotRecords(now time.Time) []SignedRecord {
	records := s.catalog.Records(now)
	live := make(map[Key]bool, len(records))
	for _, w := range records {
		var record Record
		if json.Unmarshal(w.Body, &record) == nil {
			live[record.Key] = true
		}
	}
	for key := range s.sentRecords {
		if !live[key] {
			delete(s.sentRecords, key)
		}
	}
	return records
}

// PendingIdentity reports whether a record from this peer still awaits its
// signed origin's certificate chain. A snapshot is not safe to project until
// all records sent before its completion marker have been verified.
func (s *Synchronizer) PendingIdentity() bool { return len(s.pending) != 0 }

// Receive returns replies for this peer and newly admitted records to relay.
// Invalid signatures and identities fail the link; ordinary stale/expired
// records are ignored. Relaying never changes the signed absolute expiry.
func (s *Synchronizer) Receive(m Message, now time.Time) (replies []Message, changed []SignedRecord, err error) {
	if err := m.validate(); err != nil {
		return nil, nil, err
	}
	switch m.Kind {
	case "certificate":
		replies, completed, err := s.certificates.Receive(*m.Certificate, now)
		return s.certificateResult(replies, completed, err, now)
	case "snapshot-done":
		// Runtime consumes this marker after all preceding records on the
		// ordered stream have been admitted or queued for identity repair.
	case "identity-request":
		if descriptor := s.certificates.Describe(m.Principal, now); descriptor != nil {
			return []Message{{Kind: "certificate", Certificate: descriptor}}, nil, nil
		}
	case "bundle":
		principal, err := s.cache.Put(m.Bundle, now)
		if err != nil {
			return nil, nil, err
		}
		pending := s.pending[principal]
		delete(s.pending, principal)
		for _, w := range pending {
			_, admitted, err := s.Receive(Message{Kind: "record", Record: &w}, now)
			if err != nil {
				return nil, nil, err
			}
			changed = append(changed, admitted...)
		}
	case "record":
		ok, err := s.catalog.Accept(*m.Record, now)
		if errors.Is(err, ErrExpired) || errors.Is(err, ErrStale) {
			return nil, nil, nil
		}
		if errors.Is(err, ErrIdentityNeeded) {
			count, retainedBytes := 0, 0
			for _, entries := range s.pending {
				count += len(entries)
				for _, wire := range entries {
					retainedBytes += len(wire.Body) + len(wire.Signature) + len(wire.Fingerprint)
				}
			}
			incomingBytes := len(m.Record.Body) + len(m.Record.Signature) + len(m.Record.Fingerprint)
			if count >= maxPendingRecords || retainedBytes+incomingBytes > maxPendingRecordBytes {
				return nil, nil, errPendingIdentities
			}
			var record Record
			if json.Unmarshal(m.Record.Body, &record) != nil {
				return nil, nil, errors.New("invalid wendy net service record body")
			}
			first := len(s.pending[record.Key.Device]) == 0
			s.pending[record.Key.Device] = append(s.pending[record.Key.Device], cloneWire(*m.Record))
			if first {
				return []Message{{Kind: "identity-request", Principal: record.Key.Device}}, nil, nil
			}
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		s.noteReceived(*m.Record)
		if ok {
			changed = append(changed, cloneWire(*m.Record))
		}
	}
	return replies, changed, nil
}

func (s *Synchronizer) certificateResult(messages []*localmesh.CertificateMessage, completed []string, err error, now time.Time) (replies []Message, changed []SignedRecord, result error) {
	if err != nil {
		return nil, nil, err
	}
	for _, m := range messages {
		replies = append(replies, Message{Kind: "certificate", Certificate: m})
	}
	for _, principal := range completed {
		pending := s.pending[principal]
		delete(s.pending, principal)
		for _, w := range pending {
			out, admitted, err := s.Receive(Message{Kind: "record", Record: &w}, now)
			if err != nil {
				return nil, nil, err
			}
			replies = append(replies, out...)
			changed = append(changed, admitted...)
		}
	}
	return replies, changed, nil
}

func (s *Synchronizer) ContinueCertificates(now time.Time) ([]Message, []SignedRecord, error) {
	for principal, records := range s.pending {
		keep := records[:0]
		for _, wire := range records {
			var record Record
			if json.Unmarshal(wire.Body, &record) == nil && time.UnixMilli(record.Expires).After(now) {
				keep = append(keep, wire)
			}
		}
		clear(records[len(keep):])
		if len(keep) == 0 {
			delete(s.pending, principal)
		} else {
			s.pending[principal] = keep
		}
	}
	messages, completed, err := s.certificates.Poll(now)
	return s.certificateResult(messages, completed, err, now)
}

func (s *Synchronizer) Close() { s.certificates.Close() }

// Only called after Catalog.Accept has verified the artifact, including duplicates.
// A supplier already possesses its exact body and signing certificate.
func (s *Synchronizer) noteReceived(w SignedRecord) {
	var r Record
	if json.Unmarshal(w.Body, &r) != nil {
		return
	}
	stamp := sentRecordStamp{r.Generation, sha256.Sum256(append([]byte(w.Fingerprint), w.Body...))}
	if old, ok := s.sentRecords[r.Key]; !ok || old.generation < r.Generation || (old.generation == r.Generation && old.digest == stamp.digest) {
		s.sentRecords[r.Key] = stamp
	}
	if len(s.sent) >= 1024 {
		s.sent = make(map[string]bool)
	}
	s.sent[w.Fingerprint] = true
}
