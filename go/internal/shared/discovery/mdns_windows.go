//go:build windows

package discovery

import (
	"context"
	"errors"
	"fmt"
)

// BrowseMDNSServicesContinuous streams newly discovered mDNS services of the
// given type as they appear. Only macOS has a native streaming primitive
// (dns-sd); on Windows this returns errors.ErrUnsupported so callers fall
// back to polling BrowseMDNSServices.
func BrowseMDNSServicesContinuous(_ context.Context, _ string) (<-chan MDNSService, error) {
	return nil, fmt.Errorf("continuous mDNS browsing: %w", errors.ErrUnsupported)
}

// mdnsStreamBackend is the Windows implementation of the lanBackendFn seam
// (stream.go). Windows has no native mDNS streaming API this package can
// call into (unlike darwin's mDNSResponder), so it delegates to the
// hashicorp/mdns streaming backend (backend_hashicorp.go), which is primary
// on this platform.
func mdnsStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error {
	return hashicorpStreamBackend(ctx, serviceType, emit)
}
