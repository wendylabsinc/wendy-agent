//go:build !linux

package rtps

import "errors"

func withNetworkNamespace(pid uint32, _ func() bool, fn func() error) error {
	if pid != 0 {
		return errors.New("rtps: network namespaces require Linux")
	}
	return fn()
}
