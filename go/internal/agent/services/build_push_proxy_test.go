package services

import (
	"strings"
	"testing"
)

func TestValidatePushReference_AcceptsMeshRegistryForm(t *testing.T) {
	host, port, repoTag, err := validatePushReference("robot-01.acme.cloud.wendy.dev:5000/myapp:latest")
	if err != nil {
		t.Fatalf("validatePushReference: %v", err)
	}
	if host != "robot-01.acme.cloud.wendy.dev" {
		t.Errorf("host = %q", host)
	}
	if port != 5000 {
		t.Errorf("port = %d", port)
	}
	if repoTag != "myapp:latest" {
		t.Errorf("repoTag = %q", repoTag)
	}
}

// Without this, BuildImage doubles as "push an image anywhere", authenticated
// by the build host's own credentials.
func TestValidatePushReference_RejectsArbitraryRegistry(t *testing.T) {
	_, _, _, err := validatePushReference("evil.example.com:443/exfil:latest")
	if err == nil {
		t.Fatal("an arbitrary registry must be rejected")
	}
	if !strings.Contains(err.Error(), "evil.example.com") {
		t.Fatalf("error should name the rejected host, got: %v", err)
	}
}

// A suffix check that matched substrings would accept this.
func TestValidatePushReference_RejectsSuffixLookalike(t *testing.T) {
	if _, _, _, err := validatePushReference("evil-cloud.wendy.dev.attacker.com:5000/a:latest"); err == nil {
		t.Fatal("a host merely containing the mesh domain must be rejected")
	}
}

func TestValidatePushReference_RejectsMissingPort(t *testing.T) {
	if _, _, _, err := validatePushReference("robot-01.acme.cloud.wendy.dev/myapp:latest"); err == nil {
		t.Fatal("a reference without an explicit registry port must be rejected")
	}
}

func TestValidatePushReference_RejectsMissingRepo(t *testing.T) {
	if _, _, _, err := validatePushReference("robot-01.acme.cloud.wendy.dev:5000"); err == nil {
		t.Fatal("a reference with no repository must be rejected")
	}
}

func TestValidatePushReference_RejectsBadPort(t *testing.T) {
	if _, _, _, err := validatePushReference("robot-01.acme.cloud.wendy.dev:99999/a:latest"); err == nil {
		t.Fatal("an out-of-range port must be rejected")
	}
}
