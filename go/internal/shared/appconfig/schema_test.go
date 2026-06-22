package appconfig

import (
	"encoding/json"
	"testing"
)

// TestSchemaJSON_HasFrameworksAndServices verifies that the embedded
// wendy.schema.json contains top-level "frameworks" and "services" properties
// and that the ros2 domainId maximum equals the Go constant ROS2DomainIDMax
// (WDY-1700). This test acts as a sync guard: it fails when someone changes
// ROS2DomainIDMax without updating the schema.
func TestSchemaJSON_HasFrameworksAndServices(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["frameworks"]; !ok {
		t.Error("schema missing top-level 'frameworks' property")
	}
	if _, ok := props["services"]; !ok {
		t.Error("schema missing top-level 'services' property")
	}

	defs, _ := schema["$defs"].(map[string]any)
	fw, _ := defs["frameworks"].(map[string]any)
	if fw == nil {
		t.Fatal("schema missing $defs.frameworks")
	}

	// Walk $defs.frameworks.properties.ros2.properties.domainId.maximum directly.
	fwProps, _ := fw["properties"].(map[string]any)
	ros2, _ := fwProps["ros2"].(map[string]any)
	if ros2 == nil {
		t.Fatal("$defs.frameworks missing ros2 property")
	}
	ros2Props, _ := ros2["properties"].(map[string]any)
	domainId, _ := ros2Props["domainId"].(map[string]any)
	if domainId == nil {
		t.Fatal("$defs.frameworks.ros2 missing domainId property")
	}
	maxRaw, ok := domainId["maximum"]
	if !ok {
		t.Fatal("$defs.frameworks.ros2.domainId missing 'maximum'")
	}
	if got := int(maxRaw.(float64)); got != ROS2DomainIDMax {
		t.Errorf("schema domainId maximum = %d, want %d (Go constant ROS2DomainIDMax)", got, ROS2DomainIDMax)
	}

	// Verify rmw enum length matches the number of keys in ros2RMWAliases.
	rmw, _ := ros2Props["rmw"].(map[string]any)
	if rmw == nil {
		t.Fatal("$defs.frameworks.ros2 missing rmw property")
	}
	enumRaw, _ := rmw["enum"].([]any)
	if got, want := len(enumRaw), len(ros2RMWAliases); got != want {
		t.Errorf("schema rmw enum has %d entries, want %d (keys of ros2RMWAliases)", got, want)
	}
}
