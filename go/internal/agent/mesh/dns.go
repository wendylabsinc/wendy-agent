package mesh

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"
)

// meshNameRE matches the only names this server is authoritative for.
var meshNameRE = regexp.MustCompile(`^device-([0-9]{1,5})\.cloud\.wendy\.dev\.$`)

// DNSServer answers device-N.cloud.wendy.dev with the device's mesh VIP and
// forwards every other query to the host's upstream resolver. One UDP
// listener is bound per app bridge gateway address, refcounted by the number
// of running meshed containers behind that bridge.
type DNSServer struct {
	logger   *zap.Logger
	upstream string // host resolver, e.g. "127.0.0.53:53"; empty → SERVFAIL for non-mesh names
	port     int    // 53 in production; overridable for tests

	mu        sync.Mutex
	listeners map[string]*gatewayListener
}

type gatewayListener struct {
	refs int
	srv  *dns.Server
	addr string
}

func NewDNSServer(logger *zap.Logger, upstream string) *DNSServer {
	return &DNSServer{
		logger:    logger,
		upstream:  upstream,
		port:      53,
		listeners: make(map[string]*gatewayListener),
	}
}

// EnsureListener binds a UDP DNS listener on gatewayIP (idempotent,
// refcounted). Callers pair every EnsureListener with a ReleaseListener.
func (s *DNSServer) EnsureListener(gatewayIP string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.listeners[gatewayIP]; ok {
		l.refs++
		return nil
	}
	pc, err := net.ListenPacket("udp", net.JoinHostPort(gatewayIP, strconv.Itoa(s.port)))
	if err != nil {
		return fmt.Errorf("mesh dns: listen %s: %w", gatewayIP, err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(s.handle)}
	go func() {
		if err := srv.ActivateAndServe(); err != nil {
			s.logger.Warn("mesh dns listener exited", zap.String("gateway", gatewayIP), zap.Error(err))
		}
	}()
	s.listeners[gatewayIP] = &gatewayListener{refs: 1, srv: srv, addr: pc.LocalAddr().String()}
	s.logger.Info("mesh dns listening", zap.String("addr", pc.LocalAddr().String()))
	return nil
}

// ReleaseListener drops one reference; the listener shuts down when the last
// meshed container behind that gateway stops. Releasing an unknown gateway is
// a no-op.
func (s *DNSServer) ReleaseListener(gatewayIP string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.listeners[gatewayIP]
	if !ok {
		return
	}
	l.refs--
	if l.refs > 0 {
		return
	}
	delete(s.listeners, gatewayIP)
	if err := l.srv.Shutdown(); err != nil {
		s.logger.Warn("mesh dns shutdown", zap.String("gateway", gatewayIP), zap.Error(err))
	}
}

// listenerAddr returns the bound address for a gateway, or "" if not
// listening. Used by tests.
func (s *DNSServer) listenerAddr(gatewayIP string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.listeners[gatewayIP]; ok {
		return l.addr
	}
	return ""
}

func (s *DNSServer) handle(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) != 1 {
		s.reply(w, r, dns.RcodeRefused)
		return
	}
	q := r.Question[0]
	m := meshNameRE.FindStringSubmatch(strings.ToLower(q.Name))
	if m == nil {
		s.forward(w, r)
		return
	}
	id, err := strconv.ParseInt(m[1], 10, 32)
	if err != nil {
		s.reply(w, r, dns.RcodeNameError)
		return
	}
	vip, err := VIPForDevice(int32(id))
	if err != nil {
		s.reply(w, r, dns.RcodeNameError)
		return
	}
	resp := new(dns.Msg)
	resp.SetReply(r)
	resp.Authoritative = true
	if q.Qtype == dns.TypeA {
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.IP(vip.AsSlice()),
		})
	}
	// AAAA and other types for a valid mesh name: NOERROR with no answers.
	_ = w.WriteMsg(resp)
}

func (s *DNSServer) forward(w dns.ResponseWriter, r *dns.Msg) {
	if s.upstream == "" {
		s.reply(w, r, dns.RcodeServerFailure)
		return
	}
	c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	resp, _, err := c.Exchange(r, s.upstream)
	if err != nil {
		s.reply(w, r, dns.RcodeServerFailure)
		return
	}
	_ = w.WriteMsg(resp)
}

func (s *DNSServer) reply(w dns.ResponseWriter, r *dns.Msg, rcode int) {
	m := new(dns.Msg)
	m.SetRcode(r, rcode)
	_ = w.WriteMsg(m)
}
