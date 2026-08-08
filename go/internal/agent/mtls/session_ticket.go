package mtls

import (
	"crypto/tls"
	"encoding/binary"
	"time"
)

// ticketMetaPrefix tags this package's entry in SessionState.Extra. Extra is a
// shared list any component may append to, so the prefix both namespaces our
// entry and versions its layout; UnwrapSession treats an absent or
// unknown-version entry as "decline resumption".
const ticketMetaPrefix = "wendy-mtls/1:"

// wireSessionTicketChecks installs WrapSession/UnwrapSession on a server TLS
// config so that session tickets carry the verified client cert's validity
// window and resumption is DECLINED — never failed — once that window lapses.
//
// Rationale: a resumed TLS 1.3 handshake skips the certificate exchange, so
// the full ML-DSA chain verification from the original handshake is trusted
// for the ticket's lifetime (≤7 days, less on agent restart). The cheap
// re-check here bounds that trust by the cert's own validity window. Declining
// (returning nil, nil) downgrades to a full handshake, where
// VerifyPeerCertificate re-runs the complete verification and surfaces the
// existing error paths if the cert is genuinely bad — a stale ticket
// self-heals instead of hard-failing on every retry.
//
// now is injectable for handshake-level tests; production passes time.Now.
func wireSessionTicketChecks(cfg *tls.Config, notBeforeFloor time.Time, now func() time.Time) {
	cfg.WrapSession = func(cs tls.ConnectionState, ss *tls.SessionState) ([]byte, error) {
		appendClientCertWindow(ss, cs)
		return cfg.EncryptTicket(cs, ss)
	}
	cfg.UnwrapSession = func(identity []byte, cs tls.ConnectionState) (*tls.SessionState, error) {
		ss, err := cfg.DecryptTicket(identity, cs)
		if err != nil || ss == nil {
			return nil, nil // undecryptable (e.g. pre-restart ticket) → full handshake
		}
		notBefore, notAfter, ok := clientCertWindowFromExtra(ss)
		if !ok || !resumableClientWindow(notBefore, notAfter, now(), notBeforeFloor) {
			return nil, nil
		}
		return ss, nil
	}
}

// appendClientCertWindow stamps the verified client leaf's validity window
// into the session state. With no peer cert (should not happen under
// RequireAnyClientCert) nothing is appended, which UnwrapSession later reads
// as "decline".
func appendClientCertWindow(ss *tls.SessionState, cs tls.ConnectionState) {
	if len(cs.PeerCertificates) == 0 {
		return
	}
	leaf := cs.PeerCertificates[0]
	meta := make([]byte, len(ticketMetaPrefix)+16)
	copy(meta, ticketMetaPrefix)
	binary.BigEndian.PutUint64(meta[len(ticketMetaPrefix):], uint64(leaf.NotBefore.Unix()))
	binary.BigEndian.PutUint64(meta[len(ticketMetaPrefix)+8:], uint64(leaf.NotAfter.Unix()))
	ss.Extra = append(ss.Extra, meta)
}

func clientCertWindowFromExtra(ss *tls.SessionState) (notBefore, notAfter time.Time, ok bool) {
	for _, entry := range ss.Extra {
		if len(entry) != len(ticketMetaPrefix)+16 || string(entry[:len(ticketMetaPrefix)]) != ticketMetaPrefix {
			continue
		}
		nb := int64(binary.BigEndian.Uint64(entry[len(ticketMetaPrefix):]))
		na := int64(binary.BigEndian.Uint64(entry[len(ticketMetaPrefix)+8:]))
		return time.Unix(nb, 0), time.Unix(na, 0), true
	}
	return time.Time{}, time.Time{}, false
}

// resumableClientWindow mirrors buildVerifyPeerCertificate's clock handling:
// expiry is checked against the real clock (the floor must never mask real
// expiry), NotBefore against effectiveVerificationTime (the floor rescues
// devices whose clock lags NTP).
func resumableClientWindow(notBefore, notAfter, realNow, floor time.Time) bool {
	if realNow.After(notAfter) {
		return false
	}
	return !effectiveVerificationTime(realNow, floor, notBefore).Before(notBefore)
}
