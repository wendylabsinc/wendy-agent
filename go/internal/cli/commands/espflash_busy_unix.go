//go:build darwin || linux

package commands

import (
	"os/exec"
	"strconv"
	"strings"
)

// findPortHolders shells out to lsof to find PIDs with port open, then ps to
// get a human-readable command line for each. Any failure (lsof missing, no
// matches, permission) degrades to no holders found — the caller falls back
// to a generic hint rather than surfacing a diagnostic-tool failure.
func findPortHolders(port string) []portHolder {
	out, err := exec.Command("lsof", "-t", port).Output()
	if err != nil {
		return nil
	}

	var holders []portHolder
	for _, field := range strings.Fields(string(out)) {
		pid, convErr := strconv.Atoi(field)
		if convErr != nil {
			continue
		}
		holders = append(holders, portHolder{pid: pid, command: processCommand(pid)})
	}
	return holders
}

// processCommand returns pid's full command line, or "" if the lookup fails.
func processCommand(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
