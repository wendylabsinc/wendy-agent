package services

import (
	"testing"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// Tests for validateIsolationMode

func TestValidateIsolationModeAcceptsKnownModes(t *testing.T) {
	modes := []agentpb.IsolationMode{
		agentpb.IsolationMode_ISOLATION_MODE_ISOLATED,
		agentpb.IsolationMode_ISOLATION_MODE_SHARED_NETWORK,
		agentpb.IsolationMode_ISOLATION_MODE_SHARED_IPC,
	}
	for _, m := range modes {
		if err := validateIsolationMode(m); err != nil {
			t.Errorf("validateIsolationMode(%v) = %v, want nil", m, err)
		}
	}
}

func TestValidateIsolationModeRejectsUnspecified(t *testing.T) {
	err := validateIsolationMode(agentpb.IsolationMode_ISOLATION_MODE_UNSPECIFIED)
	if err == nil {
		t.Fatal("expected error for ISOLATION_MODE_UNSPECIFIED, got nil")
	}
}

func TestValidateIsolationModeRejectsUnknown(t *testing.T) {
	err := validateIsolationMode(agentpb.IsolationMode(999))
	if err == nil {
		t.Fatal("expected error for unknown isolation mode, got nil")
	}
}

// Tests for validateServiceConfig

func validServiceConfig() *agentpb.ServiceConfig {
	return &agentpb.ServiceConfig{
		ServiceName: "my-svc",
		ImageName:   "myimage",
	}
}

func TestValidateServiceConfigAcceptsMinimalValid(t *testing.T) {
	if err := validateServiceConfig(validServiceConfig()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateServiceConfigRequiresServiceName(t *testing.T) {
	svc := validServiceConfig()
	svc.ServiceName = ""
	if err := validateServiceConfig(svc); err == nil {
		t.Fatal("expected error for empty service_name")
	}
}

func TestValidateServiceConfigRequiresImageName(t *testing.T) {
	svc := validServiceConfig()
	svc.ImageName = ""
	if err := validateServiceConfig(svc); err == nil {
		t.Fatal("expected error for empty image_name")
	}
}

func TestValidateServiceConfigServiceNameContentValidation(t *testing.T) {
	for _, bad := range []string{"bad name", "bad;name", "../etc", "a b"} {
		svc := validServiceConfig()
		svc.ServiceName = bad
		if err := validateServiceConfig(svc); err == nil {
			t.Errorf("expected error for service_name %q", bad)
		}
	}
}

func TestValidateServiceConfigImageNameAcceptsRegistryWithPort(t *testing.T) {
	for _, good := range []string{
		"myimage",
		"myimage:tag",
		"org/myimage:tag",
		"localhost:5000/myimage:tag",
		"registry.example.com:443/org/image",
		"registry.example.com/org/image:latest",
	} {
		svc := validServiceConfig()
		svc.ImageName = good
		if err := validateServiceConfig(svc); err != nil {
			t.Errorf("unexpected error for image_name %q: %v", good, err)
		}
	}
}

func TestValidateServiceConfigRejectsInvalidImageName(t *testing.T) {
	for _, bad := range []string{"UPPERCASE", "../etc/passwd", "image;rm -rf", "image name"} {
		svc := validServiceConfig()
		svc.ImageName = bad
		if err := validateServiceConfig(svc); err == nil {
			t.Errorf("expected error for image_name %q", bad)
		}
	}
}

func TestValidateServiceConfigRejectsOversizedAppConfig(t *testing.T) {
	svc := validServiceConfig()
	svc.AppConfig = make([]byte, maxAppConfigBytes+1)
	if err := validateServiceConfig(svc); err == nil {
		t.Fatal("expected error for oversized app_config")
	}
}

func TestValidateServiceConfigRejectsOversizedEnvValue(t *testing.T) {
	svc := validServiceConfig()
	svc.Env = map[string]string{"KEY": string(make([]byte, maxEnvValueBytes+1))}
	if err := validateServiceConfig(svc); err == nil {
		t.Fatal("expected error for oversized env value")
	}
}

func TestValidateServiceConfigRejectsEnvKeyWithEquals(t *testing.T) {
	svc := validServiceConfig()
	svc.Env = map[string]string{"KEY=BAD": "value"}
	if err := validateServiceConfig(svc); err == nil {
		t.Fatal("expected error for env key containing '='")
	}
}

func TestValidateServiceConfigRejectsUserArgsWithNullByte(t *testing.T) {
	svc := validServiceConfig()
	svc.UserArgs = []string{"valid", "bad\x00arg"}
	if err := validateServiceConfig(svc); err == nil {
		t.Fatal("expected error for user_arg containing null byte")
	}
}

func TestValidateServiceConfigRejectsUserArgsWithNewline(t *testing.T) {
	svc := validServiceConfig()
	svc.UserArgs = []string{"bad\narg"}
	if err := validateServiceConfig(svc); err == nil {
		t.Fatal("expected error for user_arg containing newline")
	}
}

func TestValidateServiceConfigRejectsTooManyDependsOn(t *testing.T) {
	svc := validServiceConfig()
	svc.DependsOn = make([]string, maxDependsOn+1)
	for i := range svc.DependsOn {
		svc.DependsOn[i] = "svc"
	}
	if err := validateServiceConfig(svc); err == nil {
		t.Fatal("expected error for too many depends_on entries")
	}
}

func TestValidateServiceConfigRejectsDependsOnWithSpecialChars(t *testing.T) {
	svc := validServiceConfig()
	svc.DependsOn = []string{"../other"}
	if err := validateServiceConfig(svc); err == nil {
		t.Fatal("expected error for depends_on entry with path traversal")
	}
}
