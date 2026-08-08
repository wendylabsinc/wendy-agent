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
