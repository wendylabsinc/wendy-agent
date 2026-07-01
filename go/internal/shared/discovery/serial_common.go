package discovery

import (
	"fmt"
	"time"
)

// SerialPortInfo holds a serial port path and its USB connection time.
type SerialPortInfo struct {
	Port           string
	ConnectionTime time.Time
}

// ResolveESP32SerialPort returns the best available ESP32 serial port,
// preferring the most recently connected when ConnectionTime is available.
func ResolveESP32SerialPort() (string, error) {
	devices, err := ResolveESP32SerialPorts()
	if err != nil {
		return "", err
	}
	if len(devices) == 0 {
		return "", fmt.Errorf("no ESP32 serial port found")
	}
	best := devices[0]
	for _, d := range devices[1:] {
		if d.ConnectionTime.After(best.ConnectionTime) {
			best = d
		}
	}
	return best.Port, nil
}
