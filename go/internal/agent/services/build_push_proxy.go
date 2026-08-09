package services

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PeerDialer opens a byte stream to a port on another device in this org,
// LAN-direct when possible and via the cloud broker otherwise. Satisfied by
// services.MeshDialer.
//
// Declared here rather than taking *MeshDialer so the build service can be
// tested without a broker, matching how mesh.Proxy declares its own dialer.
type PeerDialer interface {
	DialDevice(ctx context.Context, deviceID int32, port uint16) (net.Conn, string, error)
}

// pushProxy forwards loopback connections to a peer device's registry over
// mTLS, so buildkitd can push plaintext to localhost and needs no per-registry
// client certificates of its own.
//
// It records the FIRST outbound failure. Without that, a proxy that cannot
// reach its target still accepts the local connection and then closes it, so
// the pusher sees only "connection reset by peer" on 127.0.0.1 — a message that
// cannot distinguish an unreachable peer from a rejected certificate, which are
// the two causes with entirely different fixes.
type pushProxy struct {
	addr string
	stop func()
	// dial opens the outbound hop. A field so tests can relay over plain TCP;
	// production dials the mesh peer and wraps the result in TLS.
	dial func(ctx context.Context) (net.Conn, error)

	mu  sync.Mutex
	err error
}

// firstError returns the first outbound failure seen, or nil if the proxy never
// failed (including the case where nothing ever connected to it).
func (p *pushProxy) firstError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *pushProxy) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		p.err = err
	}
}

// validatePushTarget checks a push destination before any build runs.
//
// There is no hostname to constrain here: an asset id can only ever address a
// device in this org through the peer dialer, so the "push an image anywhere"
// hazard a free-form registry string carried is structurally absent. What is
// left is shape — a positive id, a real port, and a repository that is a bare
// "repo:tag" with no host smuggled into it.
func validatePushTarget(t *agentpbv2.PushTarget) error {
	if t == nil {
		return status.Error(codes.InvalidArgument, "build spec carries no push target")
	}
	if t.GetAssetId() <= 0 {
		return status.Errorf(codes.InvalidArgument, "push target has an invalid asset id %d", t.GetAssetId())
	}
	if p := t.GetRegistryPort(); p == 0 || p > 65535 {
		return status.Errorf(codes.InvalidArgument, "push target has an invalid registry port %d", p)
	}
	repo := t.GetRepository()
	if repo == "" {
		return status.Error(codes.InvalidArgument, "push target has no repository")
	}
	// A slash would make the first element a registry host once joined to the
	// proxy address, quietly redirecting the push somewhere else.
	if strings.ContainsAny(repo, "/ ") {
		return status.Errorf(codes.InvalidArgument, "push target repository %q must be a bare repository:tag", repo)
	}
	return nil
}

// startPushProxy listens on loopback and forwards each accepted connection to
// the target device's registry, dialed through the mesh by asset id and then
// wrapped in mTLS with this host's client certificate.
//
// This does not change how the image is named: the target's registry derives
// its image prefix from its own listen address, so the image lands there as
// localhost:<regPort>/<repo>:<tag> regardless of how the pusher reached it.
func startPushProxy(ctx context.Context, dialer PeerDialer, target *agentpbv2.PushTarget, tlsCfg *tls.Config) (*pushProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting push proxy: %w", err)
	}
	p := &pushProxy{
		addr: ln.Addr().String(),
		stop: func() { _ = ln.Close() },
		dial: func(ctx context.Context) (net.Conn, error) {
			raw, _, derr := dialer.DialDevice(ctx, target.GetAssetId(), uint16(target.GetRegistryPort()))
			if derr != nil {
				return nil, derr
			}
			// The mesh carries bytes; the registry still speaks mTLS on top.
			return tls.Client(raw, tlsCfg), nil
		},
	}
	go func() {
		for {
			local, acceptErr := ln.Accept()
			if acceptErr != nil {
				return // listener closed by stop()
			}
			go p.proxyOne(ctx, local, target.GetAssetId())
		}
	}()
	return p, nil
}

func (p *pushProxy) proxyOne(ctx context.Context, local net.Conn, assetID int32) {
	defer local.Close()
	remote, err := p.dial(ctx)
	if err != nil {
		p.recordError(fmt.Errorf("reaching device %d's registry over the mesh: %w", assetID, err))
		return
	}
	defer remote.Close()
	relayConns(local, remote)
}

// relayConns splices two connections until BOTH directions finish, signalling
// end-of-stream to the far side with a half-close where the transport supports
// one. This mirrors mesh.relayBytes, and the "both" matters: returning after
// only the first direction completes tears down a socket that still has unread
// data, which TCP signals as RST rather than FIN — reaching the peer as
// "connection reset by peer" rather than a clean end of response.
func relayConns(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		type closeWriter interface{ CloseWrite() error }
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			// No half-close available: closing outright is the only way to stop
			// the opposite copy blocking forever and leaking its goroutine.
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go cp(b, a)
	go cp(a, b)
	<-done
	<-done
}
