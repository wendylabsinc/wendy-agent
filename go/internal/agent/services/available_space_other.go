//go:build !(linux || darwin || freebsd)

package services

// availableBytes cannot be determined on this platform. Callers treat the
// false return as "unknown" and proceed rather than blocking every write.
func availableBytes(string) (int64, bool) {
	return 0, false
}
