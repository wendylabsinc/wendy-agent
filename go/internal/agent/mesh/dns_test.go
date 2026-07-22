package mesh

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"
)

// startTestDNS binds a listener on 127.0.0.1 with an ephemeral port and
// returns the bound address.
func startTestDNS(t *testing.T, upstream string) (*DNSServer, string) {
	t.Helper()
	s := NewDNSServer(zap.NewNop(), upstream)
	s.port = 0 // ephemeral for tests; production default is 53
	if err := s.EnsureListener("127.0.0.1"); err != nil {
		t.Fatalf("EnsureListener: %v", err)
	}
	t.Cleanup(func() { s.ReleaseListener("127.0.0.1") })
	return s, s.listenerAddr("127.0.0.1")
}

func query(t *testing.T, addr, name string, qtype uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	c := &dns.Client{Timeout: 2 * time.Second}
	resp, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("query %s: %v", name, err)
	}
	return resp
}

func TestDNSAnswersMeshName(t *testing.T) {
	_, addr := startTestDNS(t, "")
	resp := query(t, addr, "device-215.cloud.wendy.dev.", dns.TypeA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("rcode=%d answers=%d, want NOERROR with 1 answer", resp.Rcode, len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok || a.A.String() != "10.99.0.215" {
		t.Fatalf("answer = %v, want A 10.99.0.215", resp.Answer[0])
	}
}

func TestDNSMeshNameAAAAEmptyNoError(t *testing.T) {
	_, addr := startTestDNS(t, "")
	resp := query(t, addr, "device-215.cloud.wendy.dev.", dns.TypeAAAA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 0 {
		t.Fatalf("rcode=%d answers=%d, want NOERROR with 0 answers", resp.Rcode, len(resp.Answer))
	}
}

func TestDNSOutOfRangeIsNXDOMAIN(t *testing.T) {
	_, addr := startTestDNS(t, "")
	for _, name := range []string{"device-0.cloud.wendy.dev.", "device-70000.cloud.wendy.dev."} {
		resp := query(t, addr, name, dns.TypeA)
		if resp.Rcode != dns.RcodeNameError {
			t.Fatalf("%s: rcode=%d, want NXDOMAIN", name, resp.Rcode)
		}
	}
}

func TestDNSForwardsNonMeshNames(t *testing.T) {
	// Fake upstream that answers everything with 192.0.2.1.
	up, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstreamSrv := &dns.Server{PacketConn: up, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 5},
			A:   net.ParseIP("192.0.2.1"),
		})
		_ = w.WriteMsg(m)
	})}
	go upstreamSrv.ActivateAndServe()
	t.Cleanup(func() { _ = upstreamSrv.Shutdown() })

	_, addr := startTestDNS(t, up.LocalAddr().String())
	resp := query(t, addr, "example.com.", dns.TypeA)
	if len(resp.Answer) != 1 {
		t.Fatalf("forwarded answers = %d, want 1", len(resp.Answer))
	}
}

func TestDNSNoUpstreamIsServfail(t *testing.T) {
	_, addr := startTestDNS(t, "")
	resp := query(t, addr, "example.com.", dns.TypeA)
	if resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("rcode=%d, want SERVFAIL", resp.Rcode)
	}
}

func TestDNSListenerRefcount(t *testing.T) {
	s, _ := startTestDNS(t, "")
	if err := s.EnsureListener("127.0.0.1"); err != nil { // refs=2
		t.Fatal(err)
	}
	s.ReleaseListener("127.0.0.1") // refs=1, still listening
	if s.listenerAddr("127.0.0.1") == "" {
		t.Fatal("listener shut down while still referenced")
	}
	s.ReleaseListener("127.0.0.1") // refs=0, shut down
	if s.listenerAddr("127.0.0.1") != "" {
		t.Fatal("listener still up after last release")
	}
	// startTestDNS cleanup releases once more; make that a no-op by re-adding.
	if err := s.EnsureListener("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
}
