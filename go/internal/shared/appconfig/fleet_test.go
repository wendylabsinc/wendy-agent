package appconfig

import (
	"encoding/json"
	"testing"
)

// cameraFleetJSON mirrors the camera-fleet template's wendy-fleet.json
// (templates PR #59): thin topology that references app directories and the
// device tags each deploys to. It is the acceptance fixture for the fleet
// placement schema (WDY-1755/1776).
const cameraFleetJSON = `{
  "appId": "sh.wendy.examples.camerafleet",
  "components": {
    "camera": {
      "path": "camera",
      "tags": ["camera-*"],
      "expose": { "name": "usbcam", "port": 8000, "path": "/stream" }
    },
    "dashboard": {
      "path": "dashboard",
      "tags": ["central"],
      "discovers": [ { "component": "camera", "as": "WENDY_FLEET_PEERS" } ]
    }
  }
}`

func TestFleetManifest_Valid(t *testing.T) {
	m, err := ParseFleetManifest([]byte(cameraFleetJSON))
	if err != nil {
		t.Fatalf("ParseFleetManifest: %v", err)
	}
	if len(m.Components) != 2 {
		t.Fatalf("got %d components, want 2", len(m.Components))
	}
	cam := m.Components["camera"]
	if cam == nil || cam.Path != "camera" || len(cam.Tags) != 1 || cam.Tags[0] != "camera-*" {
		t.Fatalf("camera path/tags not parsed: %+v", cam)
	}
	if cam.Expose == nil || cam.Expose.Port != 8000 || cam.Expose.Path != "/stream" {
		t.Fatalf("camera expose not parsed: %+v", cam.Expose)
	}
	dash := m.Components["dashboard"]
	if dash == nil || dash.Path != "dashboard" || len(dash.Tags) != 1 || dash.Tags[0] != "central" {
		t.Fatalf("dashboard path/tags not parsed: %+v", dash)
	}
	if len(dash.Discovers) != 1 || dash.Discovers[0].Component != "camera" || dash.Discovers[0].As != "WENDY_FLEET_PEERS" {
		t.Fatalf("dashboard discovers not parsed: %+v", dash.Discovers)
	}
}

func TestFleetManifest_RequiresComponents(t *testing.T) {
	if _, err := ParseFleetManifest([]byte(`{"components":{}}`)); err == nil {
		t.Error("expected error for empty components")
	}
}

func TestComponent_Validation(t *testing.T) {
	tests := []struct {
		name string
		comp string
		ok   bool
	}{
		{"valid", `{ "path": "c", "tags": ["g"] }`, true},
		{"multi-tag", `{ "path": "c", "tags": ["a", "b-*"] }`, true},
		{"missing path", `{ "tags": ["g"] }`, false},
		{"missing tags", `{ "path": "c" }`, false},
		{"empty tags", `{ "path": "c", "tags": [] }`, false},
		{"empty tag string", `{ "path": "c", "tags": [""] }`, false},
		{"absolute path", `{ "path": "/c", "tags": ["g"] }`, false},
		{"escaping path", `{ "path": "../c", "tags": ["g"] }`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"components":{"c":` + tt.comp + `}}`
			_, err := ParseFleetManifest([]byte(raw))
			if tt.ok && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestComponentExpose_Validation(t *testing.T) {
	tests := []struct {
		name   string
		expose string
		ok     bool
	}{
		{"valid", `{ "name": "usbcam", "port": 8000, "path": "/stream" }`, true},
		{"port zero", `{ "port": 0 }`, false},
		{"port too high", `{ "port": 70000 }`, false},
		{"path no slash", `{ "port": 8000, "path": "stream" }`, false},
		{"bad name", `{ "port": 8000, "name": "Bad_Name" }`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"components":{"c":{"path":"c","tags":["g"],"expose":` + tt.expose + `}}}`
			_, err := ParseFleetManifest([]byte(raw))
			if tt.ok && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestDiscovers_Validation(t *testing.T) {
	// base has an exposing "cam" and a consumer "c" we attach discovers to.
	build := func(discovers string) string {
		return `{"components":{` +
			`"cam":{"path":"cam","tags":["cams"],"expose":{"port":8000}},` +
			`"c":{"path":"c","tags":["central"],"discovers":` + discovers + `}}}`
	}
	tests := []struct {
		name      string
		discovers string
		ok        bool
	}{
		{"valid", `[{ "component": "cam", "as": "WENDY_FLEET_PEERS" }]`, true},
		{"unknown component", `[{ "component": "nope", "as": "PEERS" }]`, false},
		{"reserved env var", `[{ "component": "cam", "as": "WENDY_DISCOVERY_URL" }]`, false},
		{"bad env var name", `[{ "component": "cam", "as": "1bad" }]`, false},
		{"missing as", `[{ "component": "cam" }]`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFleetManifest([]byte(build(tt.discovers)))
			if tt.ok && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestDiscovers_RejectsNonExposingComponent(t *testing.T) {
	// Discovering a component that exposes nothing is meaningless and rejected.
	raw := `{"components":{` +
		`"a":{"path":"a","tags":["x"]},` +
		`"b":{"path":"b","tags":["central"],"discovers":[{"component":"a","as":"PEERS"}]}}}`
	if _, err := ParseFleetManifest([]byte(raw)); err == nil {
		t.Fatal("expected error discovering a component with no expose, got nil")
	}
}

func TestFleetSchemaJSON_HasComponents(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(FleetSchemaJSON), &schema); err != nil {
		t.Fatalf("fleet schema is not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["components"]; !ok {
		t.Error("fleet schema missing top-level 'components' property")
	}
	defs, _ := schema["$defs"].(map[string]any)
	for _, def := range []string{"component", "componentExpose", "discoverRef"} {
		if _, ok := defs[def]; !ok {
			t.Errorf("fleet schema missing $defs.%s", def)
		}
	}
}

func TestWendySchemaJSON_NoLongerHasComponents(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if props, _ := schema["properties"].(map[string]any); props != nil {
		if _, ok := props["components"]; ok {
			t.Error("wendy.schema.json still has a 'components' property; it moved to wendy-fleet.schema.json")
		}
	}
}
