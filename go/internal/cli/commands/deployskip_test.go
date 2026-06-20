package commands

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func TestServiceFingerprintKey(t *testing.T) {
	a := serviceFingerprintKey("sh.wendy.app", "gpu")
	b := serviceFingerprintKey("sh.wendy.app", "vui")
	if a == b {
		t.Fatalf("distinct services share a fingerprint key: %q", a)
	}
	if want := "sh.wendy.app/svc/gpu"; a != want {
		t.Fatalf("serviceFingerprintKey = %q, want %q", a, want)
	}
}

// With WENDY_PUSH_SKIP=0 the planner must short-circuit before touching the
// device (so a nil conn is safe) and skip nothing.
func TestPlanServicePushSkipsDisabled(t *testing.T) {
	t.Setenv("WENDY_PUSH_SKIP", "0")
	services := map[string]*appconfig.ServiceConfig{
		"a": {Context: "./a"},
		"b": {Context: "./b"},
	}
	skip, hashes := planServicePushSkips(context.Background(), nil, t.TempDir(), "app", "devkey", "linux/arm64", services, nil)
	if len(skip) != 0 {
		t.Fatalf("expected no skips when disabled, got %v", skip)
	}
	if len(hashes) != 0 {
		t.Fatalf("expected no hashes computed when disabled, got %v", hashes)
	}
}
