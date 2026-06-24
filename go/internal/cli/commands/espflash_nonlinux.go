//go:build darwin || windows

package commands

func serialPortGroup(_ string) string {
	return ""
}
