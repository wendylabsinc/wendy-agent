//go:build !linux

package containerd

import "fmt"

func cloneSnapshotUpper(_, _ string) error {
	return fmt.Errorf("overlay snapshot reuse is only supported on Linux")
}
