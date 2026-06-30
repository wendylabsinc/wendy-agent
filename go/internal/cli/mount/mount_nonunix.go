//go:build !darwin && !linux

package mount

import (
	"context"
	"fmt"
)

func mountNFS(_ context.Context, _, _ string, _ bool) error {
	return fmt.Errorf("nfs mount is only supported on macOS and Linux")
}

func unmountPath(_ string) error {
	return fmt.Errorf("nfs mount is only supported on macOS and Linux")
}
