package discovery

import (
	"fmt"
	"time"
)

// SerialPortInfo holds a serial port path and its USB connection time.
type SerialPortInfo struct {
	Port           string
	ConnectionTime time.Time
	Transport      SerialTransport
}

// SerialTransport identifies how a host serial device reaches the ESP32.
// Native USB supports WendyCom after flashing; a UART bridge is an install-only
// transport because Wendy Lite's runtime USB protocol uses USB Serial/JTAG.
type SerialTransport uint8

const (
	SerialTransportNativeUSB SerialTransport = iota
	SerialTransportUARTBridge
)

// ResolveESP32SerialPorts returns only native USB Serial/JTAG ports suitable
// for WendyCom runtime discovery. UART bridges are intentionally excluded: they
// can flash an ESP32 but cannot carry Wendy Lite's runtime USB protocol.
func ResolveESP32SerialPorts() ([]SerialPortInfo, error) {
	devices, err := resolveESP32SerialPorts()
	if err != nil {
		return nil, err
	}
	return filterWendyComSerialPorts(devices), nil
}

// ResolveESP32InstallSerialPorts returns native USB and supported UART bridge
// candidates for the firmware installer.
func ResolveESP32InstallSerialPorts() ([]SerialPortInfo, error) {
	return resolveESP32SerialPorts()
}

// ResolveESP32InstallSerialPort returns the best firmware-install candidate.
// Native USB always wins over UART; connection time only breaks ties within
// the same transport.
func ResolveESP32InstallSerialPort() (SerialPortInfo, error) {
	devices, err := ResolveESP32InstallSerialPorts()
	if err != nil {
		return SerialPortInfo{}, err
	}
	return selectESP32InstallSerialPort(devices)
}

func filterWendyComSerialPorts(devices []SerialPortInfo) []SerialPortInfo {
	result := make([]SerialPortInfo, 0, len(devices))
	for _, device := range devices {
		if device.Transport == SerialTransportUARTBridge {
			continue
		}
		result = append(result, device)
	}
	return result
}

func selectESP32InstallSerialPort(devices []SerialPortInfo) (SerialPortInfo, error) {
	if len(devices) == 0 {
		return SerialPortInfo{}, fmt.Errorf("no ESP32 serial port found")
	}
	best := devices[0]
	for _, d := range devices[1:] {
		if best.Transport == SerialTransportUARTBridge && d.Transport == SerialTransportNativeUSB {
			best = d
			continue
		}
		if d.Transport == best.Transport && d.ConnectionTime.After(best.ConnectionTime) {
			best = d
		}
	}
	return best, nil
}
