package commands

import (
	"bufio"
	"strconv"
	"strings"
)

func formatDarwinOSVersion(productVersion string) string {
	productVersion = strings.TrimSpace(productVersion)
	if productVersion == "" {
		return ""
	}
	return "macOS " + productVersion
}

func parseLinuxOSRelease(data []byte) string {
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = unquoteOSReleaseValue(value)
	}

	if prettyName := strings.TrimSpace(values["PRETTY_NAME"]); prettyName != "" {
		return prettyName
	}
	name := strings.TrimSpace(values["NAME"])
	version := strings.TrimSpace(values["VERSION"])
	if version == "" {
		version = strings.TrimSpace(values["VERSION_ID"])
	}
	return strings.TrimSpace(name + " " + version)
}

func unquoteOSReleaseValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return value
	}
	if value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1]
	}
	if value[0] == '"' && value[len(value)-1] == '"' {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return value[1 : len(value)-1]
	}
	return value
}

func formatWindowsOSVersion(productName, displayVersion, buildNumber string) string {
	productName = strings.TrimSpace(productName)
	displayVersion = strings.TrimSpace(displayVersion)
	buildNumber = strings.TrimSpace(buildNumber)

	build, _ := strconv.Atoi(strings.SplitN(buildNumber, ".", 2)[0])
	if productName == "" {
		switch {
		case build >= 22000:
			productName = "Windows 11"
		case build >= 10240:
			productName = "Windows 10"
		default:
			productName = "Windows"
		}
	} else if build >= 22000 {
		// Windows 11 may report a Windows 10 product name through this
		// compatibility registry key. The build number is authoritative.
		productName = strings.Replace(productName, "Windows 10", "Windows 11", 1)
	}

	result := productName
	if displayVersion != "" && !strings.Contains(result, displayVersion) {
		result += " " + displayVersion
	}
	if buildNumber != "" {
		result += " (build " + buildNumber + ")"
	}
	return result
}
