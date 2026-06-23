//go:build linux

package services

import (
	"bufio"
	"os"
	"regexp"
	"runtime"
	"strings"
)

// detectDistro reads /etc/os-release (or legacy fallback files) and returns
// the distribution ID (e.g. "ubuntu", "arch", "rhel") and version (e.g. "22.04").
// Both strings are empty when the host distribution cannot be identified.
func detectDistro() (id, version string) {
	if id, version, ok := parseOSRelease("/etc/os-release"); ok {
		return id, version
	}
	if id, version, ok := parseRedHatRelease("/etc/redhat-release"); ok {
		return id, version
	}
	if version, ok := parseDebianVersion("/etc/debian_version"); ok {
		return "debian", version
	}
	return "", ""
}

// detectOS returns the Linux distribution ID when known, otherwise runtime.GOOS.
func detectOS() string {
	if id, _ := detectDistro(); id != "" {
		return id
	}
	return runtime.GOOS
}

// parseOSRelease parses a KEY="value" file (typically /etc/os-release) and
// returns the ID and VERSION_ID fields.
func parseOSRelease(path string) (id, version string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()

	vals := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		k, v, found := strings.Cut(scanner.Text(), "=")
		if !found {
			continue
		}
		vals[k] = strings.Trim(v, `"`)
	}
	id = vals["ID"]
	if id == "" {
		return "", "", false
	}
	return id, vals["VERSION_ID"], true
}

var rhRelRe = regexp.MustCompile(`(?i)(red\s*hat|centos|fedora)[^\d]*([\d.]+)`)

// parseRedHatRelease parses /etc/redhat-release used by RHEL 6 / CentOS 6.
func parseRedHatRelease(path string) (id, version string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	m := rhRelRe.FindSubmatch(data)
	if len(m) < 3 {
		return "", "", false
	}
	raw := strings.ToLower(string(m[1]))
	switch {
	case strings.Contains(raw, "red"):
		id = "rhel"
	case strings.Contains(raw, "centos"):
		id = "centos"
	default:
		id = "fedora"
	}
	return id, string(m[2]), true
}

// parseDebianVersion parses /etc/debian_version used by old Debian installs.
func parseDebianVersion(path string) (version string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", false
	}
	return v, true
}
