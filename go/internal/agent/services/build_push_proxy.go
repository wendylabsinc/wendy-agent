package services

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// meshRegistrySuffix is the domain mesh device addresses live under.
// Constraining push destinations to this form is what stops BuildImage from
// becoming a general-purpose "push an image anywhere" primitive authenticated
// by this device's credentials.
const meshRegistrySuffix = ".cloud.wendy.dev"

// validatePushReference splits a push reference into registry host, port and
// repository:tag, rejecting anything that is not a mesh device registry.
func validatePushReference(ref string) (string, int, string, error) {
	registry, repoTag, ok := strings.Cut(ref, "/")
	if !ok || repoTag == "" {
		return "", 0, "", status.Errorf(codes.InvalidArgument, "push reference %q has no repository", ref)
	}
	host, portStr, err := net.SplitHostPort(registry)
	if err != nil {
		return "", 0, "", status.Errorf(codes.InvalidArgument, "push reference %q must name an explicit registry port", ref)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, "", status.Errorf(codes.InvalidArgument, "push reference %q has an invalid registry port", ref)
	}
	// HasSuffix, not Contains: "evil-cloud.wendy.dev.attacker.com" contains the
	// mesh domain but is not in it.
	if !strings.HasSuffix(host, meshRegistrySuffix) {
		return "", 0, "", status.Errorf(codes.InvalidArgument,
			"refusing to push to %q: a build host may only push to a mesh device registry (*%s)", host, meshRegistrySuffix)
	}
	return host, port, repoTag, nil
}

// pushProxy forwards loopback connections to a device registry over mTLS.
//
// It records the FIRST outbound failure. Without that, a proxy that cannot
// reach its target still accepts the local connection and then closes it, so
// the pusher sees only "connection reset by peer" on 127.0.0.1 — a message that
// cannot distinguish an unreachable mesh from a rejected certificate, which are
// the two causes with entirely different fixes.
type pushProxy struct {
	addr string
	stop func()

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

// startPushProxy listens on loopback and forwards each accepted connection to
// target over mTLS, presenting this host's client certificate.
//
// buildkitd pushes plaintext to the returned address, which means it needs one
// static loopback allowance in its config rather than per-registry client
// certificates rewritten for whichever device this build happens to target.
//
// This does not change how the image is named: the target's registry derives
// its image prefix from its own listen address, so the image lands there as
// localhost:<regPort>/<repo>:<tag> regardless of the address the pusher used.
func startPushProxy(ctx context.Context, target string, tlsCfg *tls.Config) (*pushProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting push proxy: %w", err)
	}
	p := &pushProxy{
		addr: ln.Addr().String(),
		stop: func() { _ = ln.Close() },
	}
	go func() {
		for {
			local, acceptErr := ln.Accept()
			if acceptErr != nil {
				return // listener closed by stop()
			}
			go p.proxyOne(ctx, local, target, tlsCfg)
		}
	}()
	return p, nil
}

func (p *pushProxy) proxyOne(ctx context.Context, local net.Conn, target string, tlsCfg *tls.Config) {
	defer local.Close()
	d := &tls.Dialer{Config: tlsCfg}
	remote, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		p.recordError(fmt.Errorf("reaching device registry %s: %w", target, err))
		return
	}
	defer remote.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done
}
