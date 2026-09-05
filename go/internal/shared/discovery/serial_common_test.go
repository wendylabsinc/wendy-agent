package discovery

import (
	"testing"
	"time"
)

func TestRuntimeSerialPortsExcludeUARTBridges(t *testing.T) {
	devices := []SerialPortInfo{
		{Port: "/dev/native", Transport: SerialTransportNativeUSB},
		{Port: "/dev/uart", Transport: SerialTransportUARTBridge},
	}

	got := filterWendyComSerialPorts(devices)
	if len(got) != 1 || got[0].Port != "/dev/native" {
		t.Fatalf("runtime serial ports = %+v, want only native USB", got)
	}
}

func TestSelectESP32InstallSerialPortPrefersNativeUSB(t *testing.T) {
	older := time.Unix(100, 0)
	newer := time.Unix(200, 0)
	tests := [][]SerialPortInfo{
		{
			{Port: "/dev/uart", ConnectionTime: newer, Transport: SerialTransportUARTBridge},
			{Port: "/dev/native", ConnectionTime: older, Transport: SerialTransportNativeUSB},
		},
		{
			{Port: "/dev/native", ConnectionTime: older, Transport: SerialTransportNativeUSB},
			{Port: "/dev/uart", ConnectionTime: newer, Transport: SerialTransportUARTBridge},
		},
	}

	for _, devices := range tests {
		got, err := selectESP32InstallSerialPort(devices)
		if err != nil {
			t.Fatal(err)
		}
		if got.Port != "/dev/native" {
			t.Fatalf("selected %q from %+v, want native USB even though UART was connected later", got.Port, devices)
		}
	}
}

func TestSelectESP32InstallSerialPortUsesRecencyWithinTransport(t *testing.T) {
	devices := []SerialPortInfo{
		{Port: "/dev/native-old", ConnectionTime: time.Unix(100, 0), Transport: SerialTransportNativeUSB},
		{Port: "/dev/native-new", ConnectionTime: time.Unix(200, 0), Transport: SerialTransportNativeUSB},
	}

	got, err := selectESP32InstallSerialPort(devices)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != "/dev/native-new" {
		t.Fatalf("selected %q, want most recently connected native port", got.Port)
	}
}
