//go:build darwin || windows

package commands

import (
	"context"
	"runtime"
)

func installUnitreeG1(_ context.Context, _ unitreeG1InstallOptions) error {
	return validateUnitreeG1Host(runtime.GOOS, runtime.GOARCH)
}
