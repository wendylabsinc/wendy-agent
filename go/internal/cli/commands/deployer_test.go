package commands

import (
	"context"
	"testing"
)

func TestPrincipalUID(t *testing.T) {
	tests := []struct {
		in  string
		uid string
		ok  bool
	}{
		{"wendy/user/3VBQnKR", "3VBQnKR", true},
		{"wendy/user/42 (org 7)", "42", true}, // URN-derived form
		{"wendy/asset/9 (org 7)", "", false},  // not a user principal
		{"wendy/user/", "", false},            // empty uid
		{"", "", false},
		{"someone-else", "", false},
	}
	for _, tt := range tests {
		uid, ok := principalUID(tt.in)
		if ok != tt.ok || uid != tt.uid {
			t.Errorf("principalUID(%q) = (%q, %v); want (%q, %v)", tt.in, uid, ok, tt.uid, tt.ok)
		}
	}
}

func TestDeployerResolver_SelfAndFallback(t *testing.T) {
	// No auth wired (auth nil) → cloud lookups are skipped, so we exercise the
	// self-match and raw-fallback paths deterministically without a network.
	r := &deployerResolver{cache: map[string]string{}, selfUID: "me123", auth: nil}

	if got := r.Resolve(context.Background(), ""); got != "" {
		t.Errorf("empty principal = %q; want empty", got)
	}
	if got := r.Resolve(context.Background(), "wendy/user/me123"); got != "you" {
		t.Errorf("self principal = %q; want \"you\"", got)
	}
	// Unknown user, no cloud → falls back to the raw principal.
	raw := "wendy/user/other999"
	if got := r.Resolve(context.Background(), raw); got != raw {
		t.Errorf("fallback = %q; want raw %q", got, raw)
	}
	// Non-user principal is shown as-is.
	if got := r.Resolve(context.Background(), "wendy/asset/5 (org 2)"); got != "wendy/asset/5 (org 2)" {
		t.Errorf("asset principal = %q; want as-is", got)
	}
}
