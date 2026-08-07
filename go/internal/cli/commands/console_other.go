//go:build !windows

package commands

// PauseBeforeExitIfSoleConsole is Windows-only: only there does the flow
// relaunch itself into a console window that closes with the process. See
// console_windows.go.
func PauseBeforeExitIfSoleConsole() {}
