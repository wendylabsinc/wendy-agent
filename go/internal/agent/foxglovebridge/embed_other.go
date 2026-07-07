//go:build !arm64 && !amd64

package foxglovebridge

func init() { binaries = map[string][]byte{} }
