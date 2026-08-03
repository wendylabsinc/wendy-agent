//go:build darwin || windows

package commands

import (
	"context"
	"fmt"
	"runtime"
)

func installUnitreeG1(_ context.Context, _ unitreeG1InstallOptions) error {
	return fmt.Errorf("Unitree G1 flashing currently requires an Ubuntu x86-64 host; this host is %s/%s", runtime.GOOS, runtime.GOARCH)
}
