//go:build windows

package discovery

import "context"

// mdnsStreamBackend is the Windows implementation of the lanBackendFn and
// browseBackendFn seams (stream.go, mdns.go). Windows has no native mDNS
// streaming API this package can call into (unlike darwin's mDNSResponder),
// so it delegates to the hashicorp/mdns streaming backend
// (backend_hashicorp.go), which is primary on this platform.
func mdnsStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error {
	return hashicorpStreamBackend(ctx, serviceType, emit)
}
