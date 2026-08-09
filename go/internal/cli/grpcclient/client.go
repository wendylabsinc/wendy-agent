// Package grpcclient provides a gRPC client factory for connecting to the Wendy agent.
package grpcclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"

	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tlscache"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

const (
	// Stream/connection flow-control windows are intentionally small so that
	// gRPC backpressure reaches the agent's Send() within ~250ms when the
	// consumer falls behind, engaging the camera pipeline's agent-side
	// frame-dropping. The floor (~128KB) keeps a single 1080p IDR (50–150KB)
	// from stalling on the window.
	grpcInitialStreamWindow = 256 * 1024
	grpcInitialConnWindow   = 512 * 1024
	grpcReadBufferSize      = 256 * 1024
	grpcWriteBufferSize     = 256 * 1024

	// NOTE: Keep direct-agent pings conservative. macOS agents may close
	// long-running build/deploy/log streams with ENHANCE_YOUR_CALM/too_many_pings
	// when clients ping near the server's HTTP/2 keepalive policy floor. This is
	// intentionally global for now because direct-agent connections share the same
	// gRPC client path; a target-specific keepalive policy can be added later if
	// we see availability regressions.
	grpcKeepaliveTime    = 15 * time.Minute
	grpcKeepaliveTimeout = 10 * time.Second
)

// tlsDebugWriter is where WENDY_TLS_DEBUG resumption logging is written.
// Overridable in tests to capture output.
var tlsDebugWriter io.Writer = os.Stderr

type AgentConnection struct {
	Conn *grpc.ClientConn
	Host string // hostname or IP of the connected agent
	// Addr is the full host:port this connection dialed — the endpoint that
	// actually answered, mTLS port included. Empty for unix-socket and
	// pre-built (NewFromConn) connections.
	Addr           string
	IsMTLS         bool                    // true when connected via mutual TLS
	CertInfo       *config.CertificateInfo // cert used to establish mTLS; nil for plaintext
	RegistryDialer func(context.Context, int) (net.Conn, error)
	ExtraClosers   []io.Closer
	// Reconnect re-establishes a connection to the SAME device this connection
	// targets, after the agent restarts. It is set for transports where the
	// connection identity can't be re-derived from Host alone (e.g. the cloud
	// tunnel, which is pinned to a specific asset id). nil for plain LAN
	// connections, where the caller re-dials Host directly.
	Reconnect           func(context.Context) (*AgentConnection, error)
	AgentService        agentpb.WendyAgentServiceClient
	ContainerService    agentpb.WendyContainerServiceClient
	ShellService        agentpb.WendyShellServiceClient
	AudioService        agentpb.WendyAudioServiceClient
	AudioServiceV2      agentpbv2.WendyAudioServiceClient
	VideoService        agentpb.WendyVideoServiceClient
	ProvisioningService agentpb.WendyProvisioningServiceClient
	TelemetryService    agentpb.WendyTelemetryServiceClient
	FileSyncService     agentpb.WendyFileSyncServiceClient
	TimeSyncService     agentpbv2.WendyTimeSyncServiceClient
	// observedServerOrg holds the org ID read from the device's server
	// certificate during the TLS handshake (set by the OnServerIdentity sink
	// wired in ConnectWithTLSAndPins). Written on the handshake goroutine, read
	// by callers after the first RPC returns; atomic makes that read race-free.
	// nil for connections that never install the sink (plaintext / NewFromConn).
	observedServerOrg *atomic.Int32
	// observedServerIdentity holds the device's full Wendy identity (org +
	// asset) from a server certificate this client actually VERIFIED — the
	// OnVerifiedServerIdentity sink, which fires only after the chain and org
	// checks pass. Same goroutine story as observedServerOrg above, hence the
	// atomic; a pointer so "never fired" is distinguishable from a zero
	// identity. nil for connections that never install the sink.
	observedServerIdentity *atomic.Pointer[certs.WendyIdentity]
	// identityMismatch holds the typed rejection raised inside VerifyConnection
	// when the peer failed ExpectedIdentity. gRPC's lazy dial mangles that error
	// into the first RPC's status, so the ladder reads it from here instead of
	// string-matching. nil for connections that never install the sink.
	identityMismatch *atomic.Pointer[certs.IdentityMismatchError]
}

// verifiedIdentitySink returns the OnVerifiedServerIdentity callback that
// records the device identity behind a verified server certificate. Identities
// without an org are dropped: org 0 is not a real Wendy org, so storing one
// would let ObservedServerIdentity report an identity no certificate asserted.
func verifiedIdentitySink(dst *atomic.Pointer[certs.WendyIdentity]) func(certs.WendyIdentity) {
	return func(id certs.WendyIdentity) {
		if id.OrgID == 0 {
			return
		}
		stored := id
		dst.Store(&stored)
	}
}

// identityMismatchSink returns the callback that records an ExpectedIdentity
// rejection. dst may be nil (enforcement not requested), in which case the
// returned sink is a no-op, so callers need no nil check.
func identityMismatchSink(dst *atomic.Pointer[certs.IdentityMismatchError]) func(*certs.IdentityMismatchError) {
	return func(e *certs.IdentityMismatchError) {
		if dst == nil || e == nil {
			return
		}
		dst.Store(e)
	}
}

func Connect(ctx context.Context, address string) (*AgentConnection, error) {
	conn, err := grpc.NewClient(
		grpcTarget(address),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithInitialWindowSize(grpcInitialStreamWindow),
		grpc.WithInitialConnWindowSize(grpcInitialConnWindow),
		grpc.WithReadBufferSize(grpcReadBufferSize),
		grpc.WithWriteBufferSize(grpcWriteBufferSize),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                grpcKeepaliveTime,
			Timeout:             grpcKeepaliveTimeout,
			PermitWithoutStream: false,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to agent at %s: %w", address, err)
	}

	ac := newAgentConnection(conn)
	ac.Host = hostFromAddress(address)
	ac.Addr = address
	return ac, nil
}

// ConnectUnix dials the agent over a local unix domain socket with plain h2c
// (no TLS). It is used inside an `admin`-entitled container, where the agent's
// control socket is bind-mounted in and WENDY_AGENT_SOCKET points at it. The
// socket itself is the entire trust boundary (see the admin entitlement); there
// is deliberately no authentication here.
func ConnectUnix(ctx context.Context, socketPath string) (*AgentConnection, error) {
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
	conn, err := grpc.NewClient(
		"passthrough:///unix",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithInitialWindowSize(grpcInitialStreamWindow),
		grpc.WithInitialConnWindowSize(grpcInitialConnWindow),
		grpc.WithReadBufferSize(grpcReadBufferSize),
		grpc.WithWriteBufferSize(grpcWriteBufferSize),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                grpcKeepaliveTime,
			Timeout:             grpcKeepaliveTimeout,
			PermitWithoutStream: false,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to agent at unix:%s: %w", socketPath, err)
	}
	ac := newAgentConnection(conn)
	ac.Host = "unix:" + socketPath
	return ac, nil
}

func ConnectWithTLS(ctx context.Context, address string, certInfo *config.CertificateInfo) (*AgentConnection, error) {
	return ConnectWithTLSAndPins(ctx, address, certInfo, nil)
}

// newAgentTLSConfig builds the client TLS config for one agent target,
// including the persistent session cache that lets repeat CLI invocations
// skip the full ML-DSA handshake (see specs/2026-08-07-tls-session-resumption-design.md).
func newAgentTLSConfig(
	address string,
	certInfo *config.CertificateInfo,
	pins certs.PinChecker,
	observedOrg *atomic.Int32,
	observedIdentity *atomic.Pointer[certs.WendyIdentity],
	expected *certs.WendyIdentity,
	mismatch *atomic.Pointer[certs.IdentityMismatchError],
) (*tls.Config, error) {
	// Only load the leaf cert — not the chain. Go's TLS library calls
	// x509.ParseCertificate on every cert sent in the handshake, and ML-DSA
	// chain certs (from pki-core) cause parse failures on the agent's server.
	// The agent's VerifyPeerCertificate callback verifies the client cert via
	// its own ML-DSA-aware CA pool without needing the chain in the handshake.
	keyPEM, err := certInfo.PrivateKeyPEM()
	if err != nil {
		return nil, fmt.Errorf("loading client key: %w", err)
	}
	cert, err := tls.X509KeyPair(
		[]byte(certInfo.PemCertificate),
		[]byte(keyPEM),
	)
	if err != nil {
		return nil, fmt.Errorf("loading TLS cert: %w", err)
	}
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:         certInfo.PemCertificateChain,
		ExpectedOrgID:    int32(certInfo.OrganizationID),
		PinStore:         pins,
		ExpectedIdentity: expected,
		OnServerIdentity: func(id certs.WendyIdentity) {
			if id.OrgID != 0 {
				observedOrg.Store(id.OrgID)
			}
		},
		OnVerifiedServerIdentity: verifiedIdentitySink(observedIdentity),
	})
	if err != nil {
		return nil, fmt.Errorf("building TLS verifier: %w", err)
	}
	// Record a typed ExpectedIdentity rejection before any other wrap. gRPC's
	// lazy dial mangles a VerifyConnection error into the first RPC's status,
	// so the dial ladder needs this captured here rather than parsed out of
	// that mangled error later. Placed closest to the verifier returned by
	// BuildServerVerifyConnection — ahead of the WENDY_TLS_DEBUG and
	// SetResumed wraps below — so it observes that verifier's own return
	// value, not one a later wrap may have transformed.
	sink := identityMismatchSink(mismatch)
	verifyConnBase := verifyConn
	verifyConn = func(cs tls.ConnectionState) error {
		err := verifyConnBase(cs)
		var im *certs.IdentityMismatchError
		if errors.As(err, &im) {
			sink(im)
		}
		return err
	}
	if os.Getenv("WENDY_TLS_DEBUG") != "" {
		inner := verifyConn
		verifyConn = func(cs tls.ConnectionState) error {
			fmt.Fprintf(tlsDebugWriter, "[tls-debug] %s resumed=%v\n", address, cs.DidResume)
			return inner(cs)
		}
	}
	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, //nolint:gosec — hostname bypass only; VerifyConnection validates server cert against Wendy PKI
		MinVersion:         tls.VersionTLS12,
	}
	// Session resumption: nil means caching is disabled — leaving the field
	// unset is required then, because a typed-nil *Cache in the interface
	// would panic inside crypto/tls.
	cache := tlscache.ForTarget(address, cert.Certificate[0])
	if cache != nil {
		tlsCfg.ClientSessionCache = cache
	}
	// Always-on wrapper — not gated behind WENDY_TLS_DEBUG, which only nests
	// as an inner logging layer above (verifyConn already includes it when
	// set). Records THIS connection's resumption outcome on every handshake
	// (not just resumed ones), before delegating to the inner verifier, so a
	// subsequent Put for the fresh ticket Go issues even on a resumed
	// connection does not overwrite the ticket from the last full handshake
	// (see tlscache.Cache.SetResumed's doc — without this, clients would
	// chain tickets forever and never re-run the full ML-DSA verification).
	// Calling SetResumed unconditionally (rather than only when DidResume is
	// true) matters because a single *Cache is reused by a grpc.ClientConn
	// across its internal reconnect handshakes: a later legitimate FULL
	// handshake on that same connection must clear a stale resumed=true from
	// an earlier handshake, or its fresh ticket would never persist.
	// VerifyConnection runs synchronously inside the handshake, strictly
	// BEFORE crypto/tls processes the server's post-handshake
	// NewSessionTicket message (that happens lazily on a later Read), and
	// gRPC dials/handshakes a ClientConn's transports sequentially, so
	// marking always happens-before the Put it needs to affect for that
	// handshake — no race between the two.
	innerVerify := verifyConn
	tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if cache != nil {
			cache.SetResumed(cs.DidResume)
		}
		return innerVerify(cs)
	}
	return tlsCfg, nil
}

func ConnectWithTLSAndPins(ctx context.Context, address string, certInfo *config.CertificateInfo, pins certs.PinChecker) (*AgentConnection, error) {
	return ConnectWithTLSExpecting(ctx, address, certInfo, pins, nil)
}

// ConnectWithTLSExpecting is ConnectWithTLSAndPins with a required peer
// identity. A nil expected is exactly ConnectWithTLSAndPins.
func ConnectWithTLSExpecting(ctx context.Context, address string, certInfo *config.CertificateInfo, pins certs.PinChecker, expected *certs.WendyIdentity) (*AgentConnection, error) {
	observedOrg := new(atomic.Int32)
	observedIdentity := new(atomic.Pointer[certs.WendyIdentity])
	mismatch := new(atomic.Pointer[certs.IdentityMismatchError])
	tlsCfg, err := newAgentTLSConfig(address, certInfo, pins, observedOrg, observedIdentity, expected, mismatch)
	if err != nil {
		return nil, err
	}

	conn, err := grpc.NewClient(
		grpcTarget(address),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithInitialWindowSize(grpcInitialStreamWindow),
		grpc.WithInitialConnWindowSize(grpcInitialConnWindow),
		grpc.WithReadBufferSize(grpcReadBufferSize),
		grpc.WithWriteBufferSize(grpcWriteBufferSize),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                grpcKeepaliveTime,
			Timeout:             grpcKeepaliveTimeout,
			PermitWithoutStream: false,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to agent at %s with TLS: %w", address, err)
	}

	ac := newAgentConnection(conn)
	ac.Host = hostFromAddress(address)
	ac.Addr = address
	ac.IsMTLS = true
	ac.CertInfo = certInfo
	ac.observedServerOrg = observedOrg
	ac.observedServerIdentity = observedIdentity
	ac.identityMismatch = mismatch
	return ac, nil
}

// grpcTarget converts a host:port address into a gRPC target string.
// IPv6 link-local addresses contain a zone ID with a bare "%" (e.g.
// [fe80::1%en0]:50051). grpc.NewClient parses the target as a URL, where
// "%" starts a percent-encoding sequence — "%en" is invalid hex and fails.
// We use the passthrough scheme with url.URL which correctly escapes the
// zone "%" to "%25". The passthrough resolver decodes it back to the
// original zone ID before passing it to the dialer.
//
// The address MUST be bracketed for IPv6 (e.g. [fe80::1%en0]:50051).
// As a safety net, if an unbracketed IPv6 address is received, we add
// brackets before constructing the URL so the host is unambiguous.
func grpcTarget(address string) string {
	if !strings.Contains(address, "%") {
		return address
	}

	// Ensure IPv6 address is properly bracketed. net.SplitHostPort
	// handles [host]:port but fails for bare IPv6 like
	// fe80::1%en0:50051 where the colons are ambiguous.
	if _, _, err := net.SplitHostPort(address); err != nil && !strings.HasPrefix(address, "[") {
		// Zone IDs (interface names) never contain colons, so the
		// port follows the last ":".
		if i := strings.LastIndex(address, ":"); i > 0 {
			host, port := address[:i], address[i+1:]
			address = net.JoinHostPort(host, port)
		}
	}

	u := &url.URL{Scheme: "passthrough", Path: "/" + address}
	return u.String()
}

// hostFromAddress extracts the hostname/IP from a host:port address string.
// Handles IPv6 addresses like [::1]:50051.
func hostFromAddress(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return host
}

// Close closes the underlying gRPC connection.
func (c *AgentConnection) Close() error {
	var errs []error
	if c.Conn != nil {
		errs = append(errs, c.Conn.Close())
	}
	for _, closer := range c.ExtraClosers {
		if closer != nil {
			errs = append(errs, closer.Close())
		}
	}
	return errors.Join(errs...)
}

// ObservedServerOrg returns the org ID observed in the device's server
// certificate during the TLS handshake, or (0, false) if none was observed
// (plaintext connection, cert without a Wendy identity, or a handshake that
// never reached the server certificate). Safe to call after the first RPC.
// The value comes from the peer's leaf certificate SAN and is NOT validated
// against any trust chain by this accessor; it is for diagnostic display only
// and must never be used for an authorization or trust decision.
func (c *AgentConnection) ObservedServerOrg() (int32, bool) {
	if c.observedServerOrg == nil {
		return 0, false
	}
	v := c.observedServerOrg.Load()
	return v, v != 0
}

// ObservedServerIdentity returns the device's Wendy identity (org + entity, the
// entity being "asset:<assetID>" for an agent) taken from a server certificate
// this connection VERIFIED — chain-checked against the CLI's CA and matched to
// the expected org. Returns (zero, false) for plaintext connections, certs
// carrying no Wendy identity, and handshakes that never completed.
//
// Unlike ObservedServerOrg — captured pre-verification and therefore only safe
// for diagnostics — this identity is what the peer proved, so it is the value
// device pinning compares against (see enforceDevicePin).
func (c *AgentConnection) ObservedServerIdentity() (certs.WendyIdentity, bool) {
	if c.observedServerIdentity == nil {
		return certs.WendyIdentity{}, false
	}
	id := c.observedServerIdentity.Load()
	if id == nil {
		return certs.WendyIdentity{}, false
	}
	return *id, true
}

// IdentityMismatch reports the ExpectedIdentity rejection this connection hit,
// if any. It is the ladder's signal that the *device* is wrong — as opposed to
// our certificate being wrong — and therefore that retrying with other certs or
// other ports is pointless.
func (c *AgentConnection) IdentityMismatch() (*certs.IdentityMismatchError, bool) {
	if c.identityMismatch == nil {
		return nil, false
	}
	e := c.identityMismatch.Load()
	return e, e != nil
}

func newAgentConnection(conn *grpc.ClientConn) *AgentConnection {
	return &AgentConnection{
		Conn:                conn,
		identityMismatch:    new(atomic.Pointer[certs.IdentityMismatchError]),
		AgentService:        agentpb.NewWendyAgentServiceClient(conn),
		ContainerService:    agentpb.NewWendyContainerServiceClient(conn),
		ShellService:        agentpb.NewWendyShellServiceClient(conn),
		AudioService:        agentpb.NewWendyAudioServiceClient(conn),
		AudioServiceV2:      agentpbv2.NewWendyAudioServiceClient(conn),
		VideoService:        agentpb.NewWendyVideoServiceClient(conn),
		ProvisioningService: agentpb.NewWendyProvisioningServiceClient(conn),
		TelemetryService:    agentpb.NewWendyTelemetryServiceClient(conn),
		FileSyncService:     agentpb.NewWendyFileSyncServiceClient(conn),
		TimeSyncService:     agentpbv2.NewWendyTimeSyncServiceClient(conn),
	}
}

func NewFromConn(conn *grpc.ClientConn) *AgentConnection {
	return newAgentConnection(conn)
}
