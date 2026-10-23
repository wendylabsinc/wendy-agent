// Package cliupdate contains CLI update guidance shared by terminal and MCP notices.
package cliupdate

// Command returns the installation-aware updater entry point. Keep the host
// parameter for callers compiled against the original guidance helper.
func Command(_ string) string { return "wendy update" }
