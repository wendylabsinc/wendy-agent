//go:build darwin || linux || windows

package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
)

func TestESP32UARTCandidateIsNotPresentedAsConfirmedBoard(t *testing.T) {
	message := esp32InstallCandidateMessage(discovery.SerialPortInfo{
		Port:      "/dev/uart",
		Transport: discovery.SerialTransportUARTBridge,
	})
	for _, want := range []string{"USB-UART candidate", "verified before writing"} {
		if !strings.Contains(message, want) {
			t.Fatalf("candidate message %q does not contain %q", message, want)
		}
	}
}

func TestESP32UARTWarningExplainsPostFlashTransport(t *testing.T) {
	withWiFi := esp32UARTBridgeInstallWarning(true)
	for _, want := range []string{"cannot carry Wendy's runtime USB connection", "configured WiFi", "native USB port"} {
		if !strings.Contains(withWiFi, want) {
			t.Fatalf("WiFi warning %q does not contain %q", withWiFi, want)
		}
	}

	withoutWiFi := esp32UARTBridgeInstallWarning(false)
	for _, want := range []string{"No WiFi network is configured", "native USB port", "reinstall with WiFi"} {
		if !strings.Contains(withoutWiFi, want) {
			t.Fatalf("no-WiFi warning %q does not contain %q", withoutWiFi, want)
		}
	}
}

func TestESP32UARTWithoutWiFiRequiresInteractiveConfirmation(t *testing.T) {
	uart := discovery.SerialPortInfo{Transport: discovery.SerialTransportUARTBridge}
	native := discovery.SerialPortInfo{Transport: discovery.SerialTransportNativeUSB}

	if !requiresESP32UARTConfirmation(uart, false, true) {
		t.Fatal("interactive UART install without WiFi did not require confirmation")
	}
	if requiresESP32UARTConfirmation(uart, true, true) {
		t.Fatal("UART install with WiFi required confirmation")
	}
	if requiresESP32UARTConfirmation(native, false, true) {
		t.Fatal("native USB install required UART confirmation")
	}
	if requiresESP32UARTConfirmation(uart, false, false) {
		t.Fatal("non-interactive UART install required an unavailable prompt")
	}
}
