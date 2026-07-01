//go:build windows

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// ResolveESP32SerialPorts returns all connected serial ports whose USB VID/PID
// match the ESP32 constants. ConnectionTime is not available on Windows and is
// left as the zero value.
func ResolveESP32SerialPorts() ([]SerialPortInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	vid := strings.ToUpper(strings.TrimPrefix(models.ESP32VendorID, "0x"))
	pid := strings.ToUpper(strings.TrimPrefix(models.ESP32ProductID, "0x"))

	script := fmt.Sprintf(
		`Get-CimInstance Win32_PnPEntity | Where-Object { $_.PNPClass -eq 'Ports' -and $_.PNPDeviceID -like 'USB\VID_%s&PID_%s*' } | Select-Object Name, PNPDeviceID, Caption | ConvertTo-Json -Compress`,
		vid, pid,
	)

	cmd := exec.CommandContext(ctx, env.PowershellExe(), "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("querying Win32_PnPEntity for ESP32 serial port: %w", err)
	}

	return parseESP32SerialPortsJSON(string(out))
}

// serialPortRegex matches a parenthesized COM port suffix such as "(COM5)".
var serialPortRegex = regexp.MustCompile(`\(COM\d+\)`)

// parseESP32SerialPortsJSON extracts all COMN port names from a JSON blob
// produced by `Get-CimInstance Win32_PnPEntity | Select-Object Name,
// PNPDeviceID, Caption | ConvertTo-Json`. Returns bare "COMN" strings with
// zero ConnectionTime (not available via Win32_PnPEntity).
func parseESP32SerialPortsJSON(jsonOut string) ([]SerialPortInfo, error) {
	trimmed := strings.TrimSpace(jsonOut)
	if trimmed == "" {
		return nil, nil
	}
	// PowerShell returns a single object (not an array) when there's one result.
	if !strings.HasPrefix(trimmed, "[") {
		trimmed = "[" + trimmed + "]"
	}

	var entries []struct {
		Name        string `json:"Name"`
		PNPDeviceID string `json:"PNPDeviceID"`
		Caption     string `json:"Caption"`
	}
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return nil, fmt.Errorf("parsing PowerShell JSON output: %w", err)
	}

	var result []SerialPortInfo
	for _, entry := range entries {
		for _, field := range []string{entry.Name, entry.Caption} {
			if match := serialPortRegex.FindString(field); match != "" {
				result = append(result, SerialPortInfo{Port: strings.Trim(match, "()")})
				break
			}
		}
	}
	return result, nil
}

// parseESP32SerialPortJSON extracts the first COMN port name from a JSON blob
// produced by `Get-CimInstance Win32_PnPEntity | Select-Object Name,
// PNPDeviceID, Caption | ConvertTo-Json`. Returns the bare "COMN" string
// (matching what os_install.go and esptool expect on Windows).
func parseESP32SerialPortJSON(jsonOut string) (string, error) {
	devices, err := parseESP32SerialPortsJSON(jsonOut)
	if err != nil {
		return "", err
	}
	if len(devices) == 0 {
		return "", noESP32SerialPortErr()
	}
	return devices[0].Port, nil
}

func noESP32SerialPortErr() error {
	return fmt.Errorf("no ESP32 serial port found (expected COM port with VID %s)", models.ESP32VendorID)
}
