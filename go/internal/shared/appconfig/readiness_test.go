package appconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Exercise the user-facing JSON contract against both the runtime validator and
// editor schema, including empty exec arrays which Go's omitempty would erase
// if tests constructed the value and marshaled it first.
func TestReadinessValidationAndSchema(t *testing.T) {
	var schemaDoc map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schemaDoc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	// This definition is self-contained. Compile it independently because other
	// existing schema definitions use ECMAScript lookaheads unsupported by Go's
	// regular-expression engine, which is this test validator's default.
	const schemaURL = "https://wendy.dev/schemas/readiness-test.json"
	if err := compiler.AddResource(schemaURL, defOf(t, schemaDoc, "readiness")); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		data  string
		valid bool
	}{
		{"timing-only compatibility", `{"timeoutSeconds":180}`, true},
		{"empty compatibility", `{}`, true},
		{"tcp", `{"tcpSocket":{"port":8080}}`, true},
		{"http default path", `{"httpGet":{"port":8080}}`, true},
		{"http empty path default", `{"httpGet":{"port":8080,"path":""}}`, true},
		{"http root", `{"httpGet":{"port":8080,"path":"/"}}`, true},
		{"http path query", `{"httpGet":{"port":8080,"path":"/health/ready?check=dependencies&name=a%20b"}}`, true},
		{"exec argv", `{"exec":["/usr/bin/check-ready","--mode","ready"]}`, true},
		{"exec empty argument", `{"exec":["/usr/bin/check-ready",""]}`, true},
		{"explicit timing", `{"tcpSocket":{"port":65535},"timeoutSeconds":3600,"periodSeconds":300,"probeTimeoutSeconds":300}`, true},
		{"zero defaults", `{"exec":["true"],"timeoutSeconds":0,"periodSeconds":0,"probeTimeoutSeconds":0}`, true},
		{"tcp port zero", `{"tcpSocket":{"port":0}}`, false},
		{"tcp port high", `{"tcpSocket":{"port":65536}}`, false},
		{"http port zero", `{"httpGet":{"port":0}}`, false},
		{"http port negative", `{"httpGet":{"port":-1}}`, false},
		{"http port high", `{"httpGet":{"port":65536}}`, false},
		{"tcp and http", `{"tcpSocket":{"port":8080},"httpGet":{"port":8080}}`, false},
		{"tcp and exec", `{"tcpSocket":{"port":8080},"exec":["true"]}`, false},
		{"http and exec", `{"httpGet":{"port":8080},"exec":["true"]}`, false},
		{"http relative path", `{"httpGet":{"port":8080,"path":"health"}}`, false},
		{"http absolute url", `{"httpGet":{"port":8080,"path":"http://example.com/health"}}`, false},
		{"http other authority", `{"httpGet":{"port":8080,"path":"//example.com/health"}}`, false},
		{"http fragment", `{"httpGet":{"port":8080,"path":"/health#fragment"}}`, false},
		{"http backslash", `{"httpGet":{"port":8080,"path":"/foo\\bar"}}`, false},
		{"http whitespace", `{"httpGet":{"port":8080,"path":"/health ready"}}`, false},
		{"http newline", `{"httpGet":{"port":8080,"path":"/health\n"}}`, false},
		{"http NUL", `{"httpGet":{"port":8080,"path":"/health\u0000"}}`, false},
		{"http invalid escape", `{"httpGet":{"port":8080,"path":"/health%xx"}}`, false},
		{"exec empty list", `{"exec":[]}`, false},
		{"exec empty command", `{"exec":[""]}`, false},
		{"exec whitespace command", `{"exec":[" \t"]}`, false},
		{"exec NUL command", `{"exec":["check\u0000ready"]}`, false},
		{"exec NUL argument", `{"exec":["check-ready","a\u0000b"]}`, false},
		{"negative timeout", `{"timeoutSeconds":-1}`, false},
		{"excessive timeout", `{"timeoutSeconds":3601}`, false},
		{"negative period", `{"periodSeconds":-1}`, false},
		{"excessive period", `{"periodSeconds":301}`, false},
		{"negative probe timeout", `{"probeTimeoutSeconds":-1}`, false},
		{"excessive probe timeout", `{"probeTimeoutSeconds":301}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg ReadinessConfig
			if err := json.Unmarshal([]byte(tc.data), &cfg); err != nil {
				t.Fatal(err)
			}
			const prefix = `services["web"].readiness`
			err := ValidateReadiness(prefix, &cfg)
			if (err == nil) != tc.valid {
				t.Errorf("ValidateReadiness() = %v, want valid=%v", err, tc.valid)
			}
			if err != nil && !strings.Contains(err.Error(), prefix) {
				t.Errorf("error does not locate service readiness: %v", err)
			}
			var value any
			if err := json.Unmarshal([]byte(tc.data), &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); (err == nil) != tc.valid {
				t.Errorf("schema validation = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}

func TestEffectiveReadiness(t *testing.T) {
	for _, cfg := range []*AppConfig{nil, {}, {Readiness: &ReadinessConfig{TimeoutSeconds: 180}}} {
		if got := EffectiveReadiness(cfg); got != nil {
			t.Errorf("EffectiveReadiness(%+v) = %+v, want no probe", cfg, got)
		}
	}
	for _, probe := range []*ReadinessConfig{
		{TCPSocket: &TCPSocketProbe{Port: 9000}},
		{HTTPGet: &HTTPGetProbe{Port: 9000, Path: "/ready"}},
		{Exec: []string{"check-ready"}},
	} {
		cfg := &AppConfig{Readiness: probe, Entitlements: []Entitlement{{Type: EntitlementHTTP, Port: 8080}}}
		if got := EffectiveReadiness(cfg); got != probe {
			t.Errorf("explicit probe replaced: got %+v, want %+v", got, probe)
		}
	}
	for _, timing := range []*ReadinessConfig{nil, {TimeoutSeconds: 180, PeriodSeconds: 3, ProbeTimeoutSeconds: 5}} {
		cfg := &AppConfig{Readiness: timing, Entitlements: []Entitlement{{Type: EntitlementHTTP, Port: 8080}}}
		want := ReadinessConfig{TCPSocket: &TCPSocketProbe{Port: 8080}}
		if timing != nil {
			want.TimeoutSeconds, want.PeriodSeconds, want.ProbeTimeoutSeconds = 180, 3, 5
		}
		if got := EffectiveReadiness(cfg); !reflect.DeepEqual(got, &want) {
			t.Errorf("synthesized readiness = %+v, want %+v", got, &want)
		}
		if cfg.Readiness != timing || (timing != nil && timing.TCPSocket != nil) {
			t.Fatal("synthesis changed original config")
		}
	}
	// An invalid explicit command is not silently replaced by port readiness.
	cfg := &AppConfig{Readiness: &ReadinessConfig{Exec: []string{}}, Entitlements: []Entitlement{{Type: EntitlementHTTP, Port: 8080}}}
	if got := EffectiveReadiness(cfg); got != cfg.Readiness || got.TCPSocket != nil {
		t.Fatal("invalid explicit exec was replaced by implicit TCP probe")
	}
}

func TestReadinessTimingDefaults(t *testing.T) {
	for _, cfg := range []*ReadinessConfig{nil, {}} {
		if cfg.Timeout() != 30*time.Second || cfg.ProbeTimeout() != 2*time.Second || cfg.Period() != time.Second {
			t.Errorf("unexpected defaults for %+v", cfg)
		}
	}
	cfg := &ReadinessConfig{TimeoutSeconds: 180, ProbeTimeoutSeconds: 5, PeriodSeconds: 3}
	if cfg.Timeout() != 180*time.Second || cfg.ProbeTimeout() != 5*time.Second || cfg.Period() != 3*time.Second {
		t.Fatal("explicit timing values not preserved")
	}
}

func TestReadinessSchemaFieldParity(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatal(err)
	}
	props := schemaProps(t, defOf(t, schema, "readiness"))
	fields := jsonFieldNames(reflect.TypeOf(ReadinessConfig{}))
	for key := range fields {
		if _, ok := props[key]; !ok {
			t.Errorf("schema omits readiness field %q", key)
		}
	}
	for key := range props {
		if !fields[key] {
			t.Errorf("schema declares unknown readiness field %q", key)
		}
	}
}
