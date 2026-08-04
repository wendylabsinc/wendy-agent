//go:build darwin

package commands

import "os/exec"

func hostOSVersion() string {
	output, err := exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
	if err != nil {
		return ""
	}
	return formatDarwinOSVersion(string(output))
}
