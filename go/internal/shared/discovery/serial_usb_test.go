package discovery

import "testing"

func TestSupportedESP32SerialUSBIDClassifiesTransport(t *testing.T) {
	native, ok := supportedESP32SerialUSBID(0x303a, 0x1001)
	if !ok || native.transport != SerialTransportNativeUSB {
		t.Fatalf("native USB classification = (%+v, %v), want supported native interface", native, ok)
	}
	bridge, ok := supportedESP32SerialUSBID(0x10c4, 0xea60)
	if !ok || bridge.transport != SerialTransportUARTBridge {
		t.Fatalf("CP210x classification = (%+v, %v), want supported UART bridge", bridge, ok)
	}
	for _, unsupported := range [][2]uint16{{0x10c4, 0xea61}, {0x0403, 0x6001}} {
		if _, ok := supportedESP32SerialUSBID(unsupported[0], unsupported[1]); ok {
			t.Fatalf("unsupported USB ID %#04x:%#04x was accepted", unsupported[0], unsupported[1])
		}
	}
}

func TestParseUSBID(t *testing.T) {
	for _, value := range []string{"0xEA60", "ea60", " EA60\n"} {
		got, err := parseUSBID(value)
		if err != nil {
			t.Fatalf("parseUSBID(%q): %v", value, err)
		}
		if got != 0xea60 {
			t.Fatalf("parseUSBID(%q) = %#04x, want %#04x", value, got, 0xea60)
		}
	}
}
