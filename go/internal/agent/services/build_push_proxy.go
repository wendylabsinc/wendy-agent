package services

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

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
func startPushProxy(ctx context.Context, target string, tlsCfg *tls.Config) (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("starting push proxy: %w", err)
	}
	go func() {
		for {
			local, acceptErr := ln.Accept()
			if acceptErr != nil {
				return // listener closed by stop()
			}
			go proxyOne(ctx, local, target, tlsCfg)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }, nil
}

func proxyOne(ctx context.Context, local net.Conn, target string, tlsCfg *tls.Config) {
	defer local.Close()
	d := &tls.Dialer{Config: tlsCfg}
	remote, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return
	}
	defer remote.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done
}
