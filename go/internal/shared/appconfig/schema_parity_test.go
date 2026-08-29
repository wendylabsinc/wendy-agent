package appconfig

import (
	"encoding/json"
	"testing"
)

// TestSchemaEntitlementKeyParity asserts that every key allowedKeys permits for
// an entitlement type is declared in that type's schema variant. The Go
// validator is the source of truth; because each schema variant sets
// "additionalProperties": false, a key missing from the schema makes editors
// falsely reject manifests the agent accepts (this happened with the camera
// entitlement's IP-camera "user"/"password" keys).
func TestSchemaEntitlementKeyParity(t *testing.T) {
	var schema struct {
		Defs struct {
			Entitlement struct {
				OneOf []struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"oneOf"`
			} `json:"entitlement"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("parse wendy.schema.json: %v", err)
	}

	variants := make(map[string]map[string]json.RawMessage)
	for _, v := range schema.Defs.Entitlement.OneOf {
		var typeConst struct {
			Const string `json:"const"`
		}
		raw, ok := v.Properties["type"]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, &typeConst); err != nil || typeConst.Const == "" {
			continue
		}
		variants[typeConst.Const] = v.Properties
	}

	for entType, keys := range allowedKeys {
		props, ok := variants[entType]
		if !ok {
			t.Errorf("entitlement type %q has no variant in wendy.schema.json", entType)
			continue
		}
		for _, key := range keys {
			if _, ok := props[key]; !ok {
				t.Errorf("entitlement %q: allowedKeys permits %q but the schema variant does not declare it (editors will falsely reject valid manifests)", entType, key)
			}
		}
	}
}
