//go:build windows

package commands

// findPortHolders has no Windows implementation: there's no OS-bundled lsof
// equivalent, so this always reports no identifiable holder. Callers fall
// back to a generic busy-port hint.
func findPortHolders(_ string) []portHolder {
	return nil
}
