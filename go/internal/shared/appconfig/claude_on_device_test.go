package appconfig

import "testing"

// TestClaudeOnDeviceExampleValidates guards the real Examples/ClaudeOnDevice
// app config: it must parse, validate, and declare the admin entitlement plus
// the two persist mounts the app relies on (OAuth token + workspace).
func TestClaudeOnDeviceExampleValidates(t *testing.T) {
	cfg, err := LoadFromFile("../../../../Examples/ClaudeOnDevice/wendy.json")
	if err != nil {
		t.Fatalf("loading claude-on-device example: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("claude-on-device wendy.json invalid: %v", err)
	}

	var admin, home, workspace, build, buildkitCache bool
	for _, e := range cfg.Entitlements {
		switch {
		case e.Type == EntitlementAdmin:
			admin = true
		case e.Type == EntitlementBuild:
			build = true
		case e.Type == EntitlementPersist && e.Path == "/root":
			home = true
		case e.Type == EntitlementPersist && e.Path == "/workspace":
			workspace = true
		case e.Type == EntitlementPersist && e.Path == "/var/lib/buildkit":
			buildkitCache = true
		}
	}
	if !admin {
		t.Error("missing admin entitlement")
	}
	if !build {
		t.Error("missing build entitlement (on-device BuildKit)")
	}
	if !home {
		t.Error("missing persist mount for /root (home: OAuth token + MCP config)")
	}
	if !workspace {
		t.Error("missing persist mount for /workspace")
	}
	if !buildkitCache {
		t.Error("missing persist mount for /var/lib/buildkit (BuildKit cache)")
	}
}
