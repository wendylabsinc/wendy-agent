package config

import (
	"encoding/json"
	"testing"
)

// TestEvaluateDevicePin covers the WDY-1149 trust anchor: a device hostname is
// pinned to its (organisation, cloud host). The pin is renewal/re-enrollment
// tolerant — only an org or cloud-host change trips it, never a routine cert
// rotation (which keeps the same org+cloud).
func TestEvaluateDevicePin(t *testing.T) {
	c := &Config{}

	if v := c.EvaluateDevicePin("wendy-thor.local", 7, "grpc.wendy.dev:443"); v != PinFirstUse {
		t.Fatalf("unpinned host: want PinFirstUse, got %v", v)
	}

	c.SetDevicePin("wendy-thor.local", 7, "grpc.wendy.dev:443")

	// Same org + cloud (e.g. a renewed or re-enrolled cert) must match.
	if v := c.EvaluateDevicePin("wendy-thor.local", 7, "grpc.wendy.dev:443"); v != PinMatch {
		t.Fatalf("same org+cloud: want PinMatch, got %v", v)
	}
	// Different org → mismatch.
	if v := c.EvaluateDevicePin("wendy-thor.local", 9, "grpc.wendy.dev:443"); v != PinMismatch {
		t.Fatalf("org change: want PinMismatch, got %v", v)
	}
	// Different cloud host → mismatch.
	if v := c.EvaluateDevicePin("wendy-thor.local", 7, "evil.example.com:443"); v != PinMismatch {
		t.Fatalf("cloud change: want PinMismatch, got %v", v)
	}
}

// TestEvaluateDevicePinNormalizesHostname ensures cosmetic hostname differences
// (case, trailing dot, .local) don't spuriously read as a different device.
func TestEvaluateDevicePinNormalizesHostname(t *testing.T) {
	c := &Config{}
	c.SetDevicePin("Wendy-Thor.local.", 7, "grpc.wendy.dev:443")
	if v := c.EvaluateDevicePin("wendy-thor", 7, "grpc.wendy.dev:443"); v != PinMatch {
		t.Fatalf("normalized host should match pin, got %v", v)
	}
}

func TestDevicePinRoundTripsThroughConfig(t *testing.T) {
	c := &Config{}
	c.SetDevicePin("wendy-thor.local", 7, "grpc.wendy.dev:443")

	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v := got.EvaluateDevicePin("wendy-thor.local", 7, "grpc.wendy.dev:443"); v != PinMatch {
		t.Fatalf("pin did not round-trip through JSON, got %v", v)
	}
}
