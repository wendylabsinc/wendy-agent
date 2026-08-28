package appconfig

import (
	"encoding/json"
	"os"
	"testing"
)

// TestWendyDataModelAppExampleValidates guards the real
// Examples/WendyDataModelApp app config: the reference app for the model
// harness must parse, validate, declare exactly the sensor-read and
// episode-write entitlements it documents, and produce no ValidateJSON
// warnings. It fails when the example and the config schema drift apart.
func TestWendyDataModelAppExampleValidates(t *testing.T) {
	const path = "../../../../Examples/WendyDataModelApp/wendy.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading model app example: %v", err)
	}
	cfg, err := LoadFromBytes(raw)
	if err != nil {
		t.Fatalf("loading model app example: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("model app wendy.json invalid: %v", err)
	}
	if warnings := ValidateJSON(raw); len(warnings) != 0 {
		t.Fatalf("model app wendy.json produced warnings: %v", warnings)
	}

	var sensorRead, episodeWrite, camera bool
	var sensorAllowlist []string
	for _, e := range cfg.Entitlements {
		switch e.Type {
		case EntitlementSensorRead:
			sensorRead = true
			sensorAllowlist = e.Allowlist
		case EntitlementEpisodeWrite:
			episodeWrite = true
		case EntitlementCamera:
			camera = true
		}
	}
	if !sensorRead {
		t.Error("missing sensor-read entitlement (sensors-in contract)")
	}
	if !episodeWrite {
		t.Error("missing episode-write entitlement (predictions-out contract)")
	}
	// The allowlist is mandatory, and the reference app is what an author
	// copies, so it must name its source rather than model a bare grant.
	if len(sensorAllowlist) == 0 {
		t.Error("the sensor-read entitlement must name the source the example subscribes to")
	}
	// The reference app must demonstrate the first-class path, not the raw
	// one. Holding the camera entitlement would let it open /dev/videoN and
	// reintroduce the device conflict the sensor-read entitlement removes —
	// the whole reason this example once needed a telemetry-only campaign.
	if camera {
		t.Error("the reference app must not hold the camera entitlement: it consumes frames through the harness")
	}

	// The published JSON schema must accept every entitlement type the
	// example declares. The episode-write entitlement was valid in Go-side
	// validation before the schema listed it; this guard keeps the two
	// surfaces from drifting again.
	consts := schemaEntitlementConsts(t)
	for _, e := range cfg.Entitlements {
		if !consts[e.Type] {
			t.Errorf("wendy.schema.json has no entitlement entry for type %q used by the example", e.Type)
		}
	}

	// Beyond the example's own entitlements, the schema must enumerate every
	// entitlement type the Go validator accepts. ValidEntitlementTypes is the
	// source of truth; a type missing from the schema makes an editor
	// validating against wendy.schema.json falsely reject a manifest the agent
	// would happily run. Walking the full list here catches the whole class of
	// Go/schema drift (this is how the episode-write and mcp gaps went
	// unnoticed).
	for _, typ := range ValidEntitlementTypes {
		if !consts[typ] {
			t.Errorf("wendy.schema.json has no entitlement entry for supported type %q (Go/schema drift)", typ)
		}
	}
}

// schemaEntitlementConsts walks $defs.entitlement.oneOf in the embedded
// wendy.schema.json and returns the set of entitlement type consts it
// declares.
func schemaEntitlementConsts(t *testing.T) map[string]bool {
	t.Helper()
	var schema struct {
		Defs struct {
			Entitlement struct {
				OneOf []struct {
					Properties struct {
						Type struct {
							Const string `json:"const"`
						} `json:"type"`
					} `json:"properties"`
				} `json:"oneOf"`
			} `json:"entitlement"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("wendy.schema.json is not valid JSON: %v", err)
	}
	consts := map[string]bool{}
	for _, variant := range schema.Defs.Entitlement.OneOf {
		if variant.Properties.Type.Const != "" {
			consts[variant.Properties.Type.Const] = true
		}
	}
	if len(consts) == 0 {
		t.Fatal("wendy.schema.json declares no entitlement type consts")
	}
	return consts
}

// TestExampleSensorProtoMatchesCanonical keeps the reference app's build-time
// copy of the sensor proto identical to the canonical one. The example
// generates its Python stubs from the copy (its Docker build context is the
// example directory, which cannot reach Proto/), so a silent divergence would
// give the app stubs for a contract the agent no longer serves.
func TestExampleSensorProtoMatchesCanonical(t *testing.T) {
	const (
		canonical = "../../../../Proto/wendy/agent/apps/v1/sensor_service.proto"
		copied    = "../../../../Examples/WendyDataModelApp/proto/wendy/agent/apps/v1/sensor_service.proto"
	)
	want, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("reading the canonical sensor proto: %v", err)
	}
	got, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("reading the example's sensor proto copy: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("Examples/WendyDataModelApp/proto/.../sensor_service.proto has drifted from Proto/.../sensor_service.proto; copy the canonical file over it")
	}
}
