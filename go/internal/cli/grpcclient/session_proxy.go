package grpcclient

import (
	"context"
	"net"
	"sync/atomic"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ConnectSessionProxy connects to a user-owned local session broker. The
// broker is the authenticated endpoint: it established and retains the mTLS
// connection to the exact device identity supplied here. Keeping that verified
// identity on AgentConnection makes the ordinary device-pin check apply to a
// broker hit exactly as it does to a fresh network connection.
func ConnectSessionProxy(ctx context.Context, socketPath, host, addr string, certInfo *config.CertificateInfo, identity certs.WendyIdentity) (*AgentConnection, error) {
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
	conn, err := grpc.NewClient(
		"passthrough:///wendy-session-broker",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithInitialWindowSize(grpcInitialStreamWindow),
		grpc.WithInitialConnWindowSize(grpcInitialConnWindow),
		grpc.WithReadBufferSize(grpcReadBufferSize),
		grpc.WithWriteBufferSize(grpcWriteBufferSize),
	)
	if err != nil {
		return nil, err
	}

	ac := newAgentConnection(conn)
	ac.Host = host
	ac.Addr = addr
	ac.IsMTLS = true
	ac.IsSessionProxy = true
	ac.CertInfo = certInfo
	ac.observedServerIdentity = newAtomicIdentity(identity)
	return ac, nil
}

func newAtomicIdentity(identity certs.WendyIdentity) *atomic.Pointer[certs.WendyIdentity] {
	dst := new(atomic.Pointer[certs.WendyIdentity])
	stored := identity
	dst.Store(&stored)
	return dst
}
