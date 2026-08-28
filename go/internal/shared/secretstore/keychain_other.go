//go:build !darwin

package secretstore

import "context"

// RunSecurity exists on all platforms so shared test helpers compile; it is
// only ever invoked by the darwin backend.
var RunSecurity = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	return nil, nil
}

// NewKeychain has no non-darwin backend; callers must handle nil.
func NewKeychain(service string) Store { return nil }
