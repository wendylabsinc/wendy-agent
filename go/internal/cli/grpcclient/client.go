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
	"strings"
	"sync/atomic"

	"time"

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

type AgentConnection struct {
	Conn           *grpc.ClientConn
	Host           string                  // hostname or IP of the connected agent
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
	AudioService        agentpb.WendyAudioServiceClient
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
	return ac, nil
}

func ConnectWithTLS(ctx context.Context, address string, certInfo *config.CertificateInfo) (*AgentConnection, error) {
	return ConnectWithTLSAndPins(ctx, address, certInfo, nil)
}

func ConnectWithTLSAndPins(ctx context.Context, address string, certInfo *config.CertificateInfo, pins certs.PinChecker) (*AgentConnection, error) {
	// Only load the leaf cert — not the chain. Go's TLS library calls
	// x509.ParseCertificate on every cert sent in the handshake, and ML-DSA
	// chain certs (from pki-core) cause parse failures on the agent's server.
	// The agent's VerifyPeerCertificate callback verifies the client cert via
	// its own ML-DSA-aware CA pool without needing the chain in the handshake.
	cert, err := tls.X509KeyPair(
		[]byte(certInfo.PemCertificate),
		[]byte(certInfo.PemPrivateKey),
	)
	if err != nil {
		return nil, fmt.Errorf("loading TLS cert: %w", err)
	}
	observedOrg := new(atomic.Int32)
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      certInfo.PemCertificateChain,
		ExpectedOrgID: int32(certInfo.OrganizationID),
		PinStore:      pins,
		OnServerIdentity: func(id certs.WendyIdentity) {
			if id.OrgID != 0 {
				observedOrg.Store(id.OrgID)
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building TLS verifier: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, //nolint:gosec — hostname bypass only; VerifyConnection validates server cert against Wendy PKI
		VerifyConnection:   verifyConn,
		MinVersion:         tls.VersionTLS12,
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
	ac.IsMTLS = true
	ac.CertInfo = certInfo
	ac.observedServerOrg = observedOrg
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

func newAgentConnection(conn *grpc.ClientConn) *AgentConnection {
	return &AgentConnection{
		Conn:                conn,
		AgentService:        agentpb.NewWendyAgentServiceClient(conn),
		ContainerService:    agentpb.NewWendyContainerServiceClient(conn),
		AudioService:        agentpb.NewWendyAudioServiceClient(conn),
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
