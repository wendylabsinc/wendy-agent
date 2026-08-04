//go:build !darwin && !linux && !windows

package commands

func hostOSVersion() string {
	return ""
}
