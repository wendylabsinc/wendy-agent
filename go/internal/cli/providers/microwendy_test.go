package providers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
)

func TestEspIdfBinaryPath(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		product string
		wantBin string // basename of the expected binary; "" means an error is expected
	}{
		{
			name: "binary named after CMake project name",
			files: map[string]string{
				"CMakeLists.txt": "cmake_minimum_required(VERSION 3.16)\ninclude($ENV{IDF_PATH}/tools/cmake/project.cmake)\nproject(myfw)\n",
				"build/myfw.bin": "firmware",
			},
			product: "my-folder",
			wantBin: "myfw.bin",
		},
		{
			name: "falls back to product without a project() declaration",
			files: map[string]string{
				"build/my-folder.bin": "firmware",
			},
			product: "my-folder",
			wantBin: "my-folder.bin",
		},
		{
			name: "missing binary",
			files: map[string]string{
				"CMakeLists.txt": "include($ENV{IDF_PATH}/tools/cmake/project.cmake)\nproject(myfw)\n",
			},
			product: "my-folder",
			wantBin: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			binPath, err := espIdfBinaryPath(dir, tt.product)
			if tt.wantBin == "" {
				if err == nil {
					t.Fatal("espIdfBinaryPath() expected an error for a missing binary")
				}
				return
			}
			if err != nil {
				t.Fatalf("espIdfBinaryPath() error = %v", err)
			}
			if want := filepath.Join(dir, "build", tt.wantBin); binPath != want {
				t.Errorf("espIdfBinaryPath() = %q, want %q", binPath, want)
			}
		})
	}
}

func TestBoardToTarget(t *testing.T) {
	// Boards currently map to targets one-to-one by name; this pins the
	// identity mapping until real board names diverge from SoC names.
	if got := boardToTarget("esp32c6"); got != "esp32c6" {
		t.Errorf("boardToTarget(esp32c6) = %q, want %q", got, "esp32c6")
	}
}

func TestSerialExternalDeviceUnresponsive(t *testing.T) {
	p := &MicroWendyProvider{}
	dev := p.serialExternalDevice(discovery.SerialDevice{Port: "/dev/cu.usbmodem2101", Responsive: false})

	if dev.ConnectionInfo["needsInstall"] != "true" {
		t.Errorf("expected needsInstall=true for an unresponsive port, got ConnectionInfo=%+v", dev.ConnectionInfo)
	}
	if dev.ConnectionInfo["serialPort"] != "/dev/cu.usbmodem2101" {
		t.Errorf("expected serialPort to be preserved, got %+v", dev.ConnectionInfo)
	}
	if !dev.IsWendyDevice {
		t.Errorf("expected an unresponsive ESP32 board to still be flagged IsWendyDevice")
	}
}

func TestSerialExternalDeviceResponsive(t *testing.T) {
	p := &MicroWendyProvider{}
	dev := p.serialExternalDevice(discovery.SerialDevice{
		Port: "/dev/cu.usbmodem2101", Responsive: true,
		ID: "esp", Name: "esp", DisplayName: "esp",
	})

	if dev.ConnectionInfo["needsInstall"] != "" {
		t.Errorf("expected no needsInstall marker for a responsive port, got ConnectionInfo=%+v", dev.ConnectionInfo)
	}
	if dev.DisplayName != "esp" {
		t.Errorf("expected the resolved identity display name to be used, got %q", dev.DisplayName)
	}
}

func TestConnectableLiteMDNSService(t *testing.T) {
	tests := []struct {
		name string
		svc  discovery.MDNSService
		want bool
	}{
		{name: "resolved", svc: discovery.MDNSService{IPAddress: "192.0.2.10", Port: 5054}, want: true},
		{name: "stale record without address", svc: discovery.MDNSService{Hostname: "old.local", Port: 5054}, want: false},
		{name: "missing port", svc: discovery.MDNSService{IPAddress: "192.0.2.10"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := connectableLiteMDNSService(tt.svc); got != tt.want {
				t.Errorf("connectableLiteMDNSService(%+v) = %v, want %v", tt.svc, got, tt.want)
			}
		})
	}
}

func TestContendedPortsError(t *testing.T) {
	tests := []struct {
		name  string
		ports []string
		want  string
	}{
		{
			name:  "single port",
			ports: []string{"/dev/cu.usbmodem101"},
			want:  "found an ESP32 serial port but couldn't open it: /dev/cu.usbmodem101 is in use by another process (e.g. a running `wendy device camera view` or `wendy run`) — stop it and try again",
		},
		{
			name:  "multiple ports",
			ports: []string{"/dev/cu.usbmodem101", "/dev/cu.usbmodem201"},
			want:  "found 2 ESP32 serial ports but couldn't open them: /dev/cu.usbmodem101, /dev/cu.usbmodem201 are in use by another process (e.g. a running `wendy device camera view` or `wendy run`) — stop it and try again",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contendedPortsError(tt.ports).Error(); got != tt.want {
				t.Errorf("contendedPortsError(%v) = %q, want %q", tt.ports, got, tt.want)
			}
		})
	}
}

func TestConnectSerialWithRetrySucceedsAfterTransientFailures(t *testing.T) {
	origFn, origDelay := connectSerialFn, serialConnectRetryDelay
	defer func() { connectSerialFn, serialConnectRetryDelay = origFn, origDelay }()
	serialConnectRetryDelay = time.Millisecond

	var attempts int
	connectSerialFn = func(_ *liteclient.WendyLiteClient, _ string) error {
		attempts++
		if attempts < serialConnectMaxAttempts {
			return errors.New("reading header: read timeout")
		}
		return nil
	}

	if err := connectSerialWithRetry(nil, "/dev/cu.usbmodem2101"); err != nil {
		t.Fatalf("connectSerialWithRetry() error = %v, want nil after eventual success", err)
	}
	if attempts != serialConnectMaxAttempts {
		t.Errorf("attempts = %d, want %d", attempts, serialConnectMaxAttempts)
	}
}

func TestConnectSerialWithRetryGivesUpAfterMaxAttempts(t *testing.T) {
	origFn, origDelay := connectSerialFn, serialConnectRetryDelay
	defer func() { connectSerialFn, serialConnectRetryDelay = origFn, origDelay }()
	serialConnectRetryDelay = time.Millisecond

	wantErr := errors.New("reading header: read timeout")
	var attempts int
	connectSerialFn = func(_ *liteclient.WendyLiteClient, _ string) error {
		attempts++
		return wantErr
	}

	err := connectSerialWithRetry(nil, "/dev/cu.usbmodem2101")
	if !errors.Is(err, wantErr) {
		t.Fatalf("connectSerialWithRetry() error = %v, want it to wrap %v", err, wantErr)
	}
	if attempts != serialConnectMaxAttempts {
		t.Errorf("attempts = %d, want %d", attempts, serialConnectMaxAttempts)
	}
	if !strings.Contains(err.Error(), "/dev/cu.usbmodem2101") {
		t.Errorf("error %q should mention the serial port", err.Error())
	}
}

func TestGetDeviceInfoShortCircuitsForNeedsInstall(t *testing.T) {
	p := &MicroWendyProvider{}
	device := p.serialExternalDevice(discovery.SerialDevice{Port: "/dev/cu.usbmodem2101", Responsive: false})

	_, err := p.GetDeviceInfo(context.Background(), device)

	var unsupported *AppRequirementsUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("GetDeviceInfo() error = %v, want an *AppRequirementsUnsupportedError", err)
	}
}
