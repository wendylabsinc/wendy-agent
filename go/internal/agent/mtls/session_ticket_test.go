package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"
)

func TestClientCertWindowRoundTrip(t *testing.T) {
	nb := time.Unix(1700000000, 0)
	na := time.Unix(1710000000, 0)
	ss := &tls.SessionState{}
	appendClientCertWindow(ss, tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{NotBefore: nb, NotAfter: na}},
	})
	gotNB, gotNA, ok := clientCertWindowFromExtra(ss)
	if !ok {
		t.Fatal("window not found after append")
	}
	if !gotNB.Equal(nb) || !gotNA.Equal(na) {
		t.Errorf("window = [%v, %v], want [%v, %v]", gotNB, gotNA, nb, na)
	}
}

func TestClientCertWindowNoPeerCertAppendsNothing(t *testing.T) {
	ss := &tls.SessionState{}
	appendClientCertWindow(ss, tls.ConnectionState{})
	if len(ss.Extra) != 0 {
		t.Errorf("Extra = %v, want empty", ss.Extra)
	}
	if _, _, ok := clientCertWindowFromExtra(ss); ok {
		t.Error("found a window in an empty session state")
	}
}

func TestClientCertWindowIgnoresForeignAndMalformedEntries(t *testing.T) {
	nb := time.Unix(1700000000, 0)
	na := time.Unix(1710000000, 0)
	ss := &tls.SessionState{Extra: [][]byte{
		[]byte("some-other-component:opaque"),
		[]byte(ticketMetaPrefix + "short"),      // right prefix, wrong length
		[]byte("wendy-mtls/2:0123456789abcdef"), // future version — must not parse as v1
	}}
	if _, _, ok := clientCertWindowFromExtra(ss); ok {
		t.Fatal("parsed a window out of foreign/malformed entries")
	}
	appendClientCertWindow(ss, tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{NotBefore: nb, NotAfter: na}},
	})
	gotNB, _, ok := clientCertWindowFromExtra(ss)
	if !ok || !gotNB.Equal(nb) {
		t.Errorf("valid entry not found among foreign entries: ok=%v nb=%v", ok, gotNB)
	}
}

func TestResumableClientWindow(t *testing.T) {
	base := time.Unix(1700000000, 0)
	nb, na := base, base.Add(30*24*time.Hour)
	cases := []struct {
		name    string
		realNow time.Time
		floor   time.Time
		want    bool
	}{
		{"inside window", base.Add(time.Hour), time.Time{}, true},
		{"expired", na.Add(time.Second), time.Time{}, false},
		{"expired despite floor", na.Add(time.Second), na.Add(48 * time.Hour), false},
		{"not yet valid, no floor", base.Add(-time.Hour), time.Time{}, false},
		{"stuck clock rescued by floor", base.Add(-30 * 24 * time.Hour), base, true},
		// Floor advancement is capped at floor+maxClockSkewTolerance: a cert
		// starting further in the future than that stays non-resumable.
		{"future cert beyond skew cap", base.Add(-30 * 24 * time.Hour), base.Add(-2 * maxClockSkewTolerance), false},
	}
	for _, tc := range cases {
		if got := resumableClientWindow(nb, na, tc.realNow, tc.floor); got != tc.want {
			t.Errorf("%s: resumable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNewClientTLSConfigSharesSessionCache(t *testing.T) {
	pki := newResumptionPKI(t)
	a, err := NewClientTLSConfig(pki.serverCertPEM, pki.caPEM, pki.serverKeyPEM, nil)
	if err != nil {
		t.Fatalf("NewClientTLSConfig: %v", err)
	}
	b, err := NewClientTLSConfig(pki.serverCertPEM, pki.caPEM, pki.serverKeyPEM, nil)
	if err != nil {
		t.Fatalf("NewClientTLSConfig: %v", err)
	}
	if a.ClientSessionCache == nil {
		t.Fatal("ClientSessionCache not set on mesh client config")
	}
	if a.ClientSessionCache != b.ClientSessionCache {
		t.Error("mesh client configs do not share one session cache; per-config caches never hit (configs are built per dial)")
	}
}
