//go:build !windows

package mount

import (
	"context"
	"fmt"
)

func mapWebdavDrive(_ context.Context, _, _ string) error {
	return fmt.Errorf("webdav drive mapping is only supported on Windows")
}

func unmapWebdavDrive(_ string) error {
	return fmt.Errorf("webdav drive mapping is only supported on Windows")
}
