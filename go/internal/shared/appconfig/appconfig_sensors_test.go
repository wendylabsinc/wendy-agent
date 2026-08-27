package appconfig

import "testing"

// TestSensorsEntitlementAcceptsAnAllowlist covers the narrowing option: the
// sensors grant may name the source ids it covers, the way the camera grant
// names device nodes.
func TestSensorsEntitlementAcceptsAnAllowlist(t *testing.T) {
	raw := []byte(`{
		"appId": "sh.wendy.test.sensors",
		"version": "1.0.0",
		"entitlements": [{"type": "sensors", "allowlist": ["v4l2:/dev/video0"]}]
	}`)
	cfg, err := LoadFromBytes(raw)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if warnings := ValidateJSON(raw); len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if got := cfg.Entitlements[0].Allowlist; len(got) != 1 || got[0] != "v4l2:/dev/video0" {
		t.Fatalf("allowlist = %v", got)
	}
}

// TestSensorsEntitlementRejectsUnknownKeys keeps the grant from silently
// accepting configuration it does not honor — a "mode" that does nothing would
// read as a restriction that is not enforced.
func TestSensorsEntitlementRejectsUnknownKeys(t *testing.T) {
	raw := []byte(`{
		"appId": "sh.wendy.test.sensors",
		"version": "1.0.0",
		"entitlements": [{"type": "sensors", "mode": "readonly"}]
	}`)
	if warnings := ValidateJSON(raw); len(warnings) == 0 {
		t.Fatal("an unknown key on the sensors entitlement produced no warning")
	}
}

// TestSensorsEntitlementRoundTripsThroughAnnotations guards the socket-restore
// path: after an agent restart the allowlist is recovered from container labels,
// not from wendy.json, so a lost allowlist would silently widen the grant.
func TestSensorsEntitlementRoundTripsThroughAnnotations(t *testing.T) {
	original := Entitlement{Type: EntitlementSensors, Allowlist: []string{"v4l2:/dev/video0", "ipcamera:200"}}
	decoded := ParseEntitlementAnnotation(EntitlementSensors, EntitlementAnnotationValue(original))
	if len(decoded.Allowlist) != 2 || decoded.Allowlist[0] != "v4l2:/dev/video0" || decoded.Allowlist[1] != "ipcamera:200" {
		t.Fatalf("allowlist after round trip = %v", decoded.Allowlist)
	}
}
