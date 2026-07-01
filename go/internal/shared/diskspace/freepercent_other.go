//go:build !linux

package diskspace

// FreePercent is unavailable off-device: the image garbage collector that uses
// it only runs on WendyOS Linux targets, and the CLI reads disk data over the
// wire rather than statfs-ing locally. It always reports (0, false) so callers
// no-op. See the Linux build for the real implementation.
func FreePercent(_ string) (float64, bool) {
	return 0, false
}
