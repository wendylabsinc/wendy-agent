package appconfig

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
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

	// Verify exact parity: each enum value must be a key in ros2RMWAliases, and vice versa.
	want := make(map[string]bool, len(ros2RMWAliases))
	for k := range ros2RMWAliases {
		want[k] = true
	}
	for _, v := range enumRaw {
		s, _ := v.(string)
		if !want[s] {
			t.Errorf("schema rmw enum value %q is not a key of ros2RMWAliases", s)
		}
		delete(want, s)
	}
	for k := range want {
		t.Errorf("ros2RMWAliases key %q is missing from schema rmw enum", k)
	}

	// Discovery scope must stay aligned with the values accepted by Go validation.
	discoveryScope, _ := ros2Props["discoveryScope"].(map[string]any)
	if discoveryScope == nil {
		t.Fatal("$defs.frameworks.ros2 missing discoveryScope property")
	}
	discoveryEnum, _ := discoveryScope["enum"].([]any)
	if len(discoveryEnum) != 2 || discoveryEnum[0] != ROS2DiscoveryScopeApp || discoveryEnum[1] != ROS2DiscoveryScopeHost {
		t.Errorf("schema discoveryScope enum = %v, want [%q %q]", discoveryEnum, ROS2DiscoveryScopeApp, ROS2DiscoveryScopeHost)
	}
}

func TestSchemaJSON_HasResources(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["resources"]; !ok {
		t.Error("schema missing top-level 'resources' property")
	}

	defs, _ := schema["$defs"].(map[string]any)
	rl, _ := defs["resourceLimits"].(map[string]any)
	if rl == nil {
		t.Fatal("schema missing $defs.resourceLimits")
	}
	rlProps, _ := rl["properties"].(map[string]any)
	for _, key := range []string{"memory", "cpus", "pids"} {
		if _, ok := rlProps[key]; !ok {
			t.Errorf("$defs.resourceLimits missing %q property", key)
		}
	}

	// The service def must also reference resourceLimits so per-service limits
	// validate in editors.
	svc, _ := defs["service"].(map[string]any)
	svcProps, _ := svc["properties"].(map[string]any)
	if _, ok := svcProps["resources"]; !ok {
		t.Error("$defs.service missing 'resources' property")
	}
}

// TestSchemaJSON_ServiceHasReadinessAndHooks verifies that per-service
// readiness and hooks (WDY-1271) are declared on $defs.service as $refs to the
// shared $defs.readiness / $defs.hooks, so editors validate x-wendy-equivalent
// service-level lifecycle config instead of rejecting it under
// additionalProperties:false.
func TestSchemaJSON_ServiceHasReadinessAndHooks(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	defs, _ := schema["$defs"].(map[string]any)
	svc, _ := defs["service"].(map[string]any)
	if svc == nil {
		t.Fatal("schema missing $defs.service")
	}
	svcProps, _ := svc["properties"].(map[string]any)

	for _, key := range []string{"readiness", "hooks"} {
		prop, ok := svcProps[key].(map[string]any)
		if !ok {
			t.Errorf("$defs.service missing %q property", key)
			continue
		}
		wantRef := "#/$defs/" + key
		if ref, _ := prop["$ref"].(string); ref != wantRef {
			t.Errorf("$defs.service.%s $ref = %q, want %q", key, ref, wantRef)
		}
		if _, ok := defs[key].(map[string]any); !ok {
			t.Errorf("schema missing $defs.%s referenced by $defs.service.%s", key, key)
		}
	}
}

func TestSchemaJSON_HTTPEntitlement(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	entitlement := defOf(t, schema, "entitlement")
	branches, ok := entitlement["oneOf"].([]any)
	if !ok {
		t.Fatal("$defs.entitlement missing oneOf")
	}

	var httpBranch map[string]any
	for _, raw := range branches {
		branch, _ := raw.(map[string]any)
		props, _ := branch["properties"].(map[string]any)
		typeProp, _ := props["type"].(map[string]any)
		if typeProp["const"] == "http" {
			httpBranch = branch
			break
		}
	}
	if httpBranch == nil {
		t.Fatal("$defs.entitlement.oneOf missing http branch")
	}
	if additional, ok := httpBranch["additionalProperties"].(bool); !ok || additional {
		t.Errorf("http entitlement additionalProperties = %v, want false", httpBranch["additionalProperties"])
	}

	required, _ := httpBranch["required"].([]any)
	requiredSet := make(map[string]bool, len(required))
	for _, key := range required {
		if name, ok := key.(string); ok {
			requiredSet[name] = true
		}
	}
	for _, key := range []string{"type", "port"} {
		if !requiredSet[key] {
			t.Errorf("http entitlement does not require %q", key)
		}
	}

	props := schemaProps(t, httpBranch)
	port, ok := props["port"].(map[string]any)
	if !ok {
		t.Fatal("http entitlement missing port property")
	}
	if got := port["type"]; got != "integer" {
		t.Errorf("http port type = %v, want integer", got)
	}
	if got := int(port["minimum"].(float64)); got != 1 {
		t.Errorf("http port minimum = %d, want 1", got)
	}
	if got := int(port["maximum"].(float64)); got != 65535 {
		t.Errorf("http port maximum = %d, want 65535", got)
	}
}

// TestSchemaJSON_IPCEntitlement is a sync guard between the schema branch
// editors validate against and the Go rules the agent enforces: the role enum
// must match ValidIPCRoles and the name pattern must accept exactly what
// ValidateIPCName accepts.
func TestSchemaJSON_IPCEntitlement(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	branch := entitlementBranch(t, schema, "ipc")
	if additional, ok := branch["additionalProperties"].(bool); !ok || additional {
		t.Errorf("ipc entitlement additionalProperties = %v, want false", branch["additionalProperties"])
	}

	required, _ := branch["required"].([]any)
	requiredSet := make(map[string]bool, len(required))
	for _, key := range required {
		if name, ok := key.(string); ok {
			requiredSet[name] = true
		}
	}
	for _, key := range []string{"type", "name", "role"} {
		if !requiredSet[key] {
			t.Errorf("ipc entitlement does not require %q", key)
		}
	}

	props := schemaProps(t, branch)
	role, ok := props["role"].(map[string]any)
	if !ok {
		t.Fatal("ipc entitlement missing role property")
	}
	roleEnum, _ := role["enum"].([]any)
	if len(roleEnum) != len(ValidIPCRoles) {
		t.Fatalf("schema role enum = %v, want %v", roleEnum, ValidIPCRoles)
	}
	for i, want := range ValidIPCRoles {
		if roleEnum[i] != want {
			t.Errorf("schema role enum[%d] = %v, want %q", i, roleEnum[i], want)
		}
	}

	name, ok := props["name"].(map[string]any)
	if !ok {
		t.Fatal("ipc entitlement missing name property")
	}
	pattern, _ := name["pattern"].(string)
	if pattern == "" {
		t.Fatal("ipc entitlement name has no pattern")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("ipc name pattern does not compile: %v", err)
	}
	for _, candidate := range []string{"world", "world-model", "a", "svc9", "World", "1world", "world-", "com.example", "../etc", ""} {
		schemaOK := re.MatchString(candidate)
		goOK := ValidateIPCName(candidate) == nil
		if schemaOK != goOK {
			t.Errorf("ipc name %q: schema pattern accepts=%v, ValidateIPCName accepts=%v", candidate, schemaOK, goOK)
		}
	}
}

// entitlementBranch returns the $defs.entitlement oneOf branch whose type const
// is entType.
func entitlementBranch(t *testing.T, schema map[string]any, entType string) map[string]any {
	t.Helper()
	entitlement := defOf(t, schema, "entitlement")
	branches, ok := entitlement["oneOf"].([]any)
	if !ok {
		t.Fatal("$defs.entitlement missing oneOf")
	}
	for _, raw := range branches {
		branch, _ := raw.(map[string]any)
		props, _ := branch["properties"].(map[string]any)
		typeProp, _ := props["type"].(map[string]any)
		if typeProp["const"] == entType {
			return branch
		}
	}
	t.Fatalf("$defs.entitlement.oneOf missing %q branch", entType)
	return nil
}

func TestSchemaJSON_DeclaresROS2ExampleKeys(t *testing.T) {
	// The flagship ROS 2 example must validate against the schema (WDY-1700):
	// every top-level key it uses must be a declared property, else
	// additionalProperties:false rejects it in editors.
	data, err := os.ReadFile("../../../../Examples/ROS2/wendy.json")
	if err != nil {
		t.Fatalf("reading ROS 2 example: %v", err)
	}
	var example map[string]any
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatalf("example is not valid JSON: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	props := schema["properties"].(map[string]any)
	for k := range example {
		if _, ok := props[k]; !ok {
			t.Errorf("ROS 2 example uses top-level key %q not declared in schema properties (additionalProperties:false would reject it)", k)
		}
	}
}

// TestSchemaJSON_MatchesStructFields is a sync guard: the schema declares
// additionalProperties:false, so any field the Go structs decode but the schema
// omits is reported as invalid by editors, and any schema-only property is
// accepted in an editor and then silently dropped at load.
func TestSchemaJSON_MatchesStructFields(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	cases := []struct {
		name  string
		props map[string]any
		want  map[string]bool
	}{
		{"top level", schemaProps(t, schema), jsonFieldNames(reflect.TypeOf(AppConfig{}))},
		{"$defs.service", schemaProps(t, defOf(t, schema, "service")), jsonFieldNames(reflect.TypeOf(ServiceConfig{}))},
	}

	for _, tc := range cases {
		for key := range tc.want {
			if _, ok := tc.props[key]; !ok {
				t.Errorf("%s: schema is missing %q, which the struct decodes", tc.name, key)
			}
		}
		for key := range tc.props {
			// $schema is an editor pointer, not a config field.
			if key == "$schema" {
				continue
			}
			if !tc.want[key] {
				t.Errorf("%s: schema declares %q, which no struct field decodes", tc.name, key)
			}
		}
	}
}

func schemaProps(t *testing.T, node map[string]any) map[string]any {
	t.Helper()
	props, ok := node["properties"].(map[string]any)
	if !ok {
		t.Fatal("node has no properties object")
	}
	return props
}

func defOf(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	defs, _ := schema["$defs"].(map[string]any)
	def, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("schema missing $defs.%s", name)
	}
	return def
}
