package platforminfo

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Prober reads OS-specific environment details. Implementations must never
// panic and should return "" when a value cannot be determined.
type Prober interface {
	OSVersion() string
	Kernel() string
}

var defaultProber Prober = osProber{}

type osProber struct{}

func (osProber) OSVersion() string {
	switch runtime.GOOS {
	case "darwin":
		return strings.TrimSpace(runCmd("sw_vers", "-productVersion"))
	case "linux":
		return linuxOSVersion()
	case "windows":
		return strings.TrimSpace(runCmd("cmd", "/c", "ver"))
	}
	return ""
}

func (osProber) Kernel() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	return strings.TrimSpace(runCmd("uname", "-sr"))
}

// linuxOSVersion parses VERSION (or VERSION_ID) from /etc/os-release.
func linuxOSVersion() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	var versionID, version string
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.TrimSpace(k) {
		case "VERSION":
			version = v
		case "VERSION_ID":
			versionID = v
		}
	}
	if version != "" {
		return version
	}
	return versionID
}

func runCmd(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
