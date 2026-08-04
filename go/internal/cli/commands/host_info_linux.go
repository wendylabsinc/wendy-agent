//go:build linux

package commands

import "os"

func hostOSVersion() string {
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		data, err := os.ReadFile(path)
		if err == nil {
			if version := parseLinuxOSRelease(data); version != "" {
				return version
			}
		}
	}
	return ""
}
