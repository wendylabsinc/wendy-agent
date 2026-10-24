package identity

import (
	"testing"
)

func TestEffectiveLoginIssuer(t *testing.T) {
	const requested = "https://auth.wendy.sh/realms/acme"
	cases := []struct {
		name     string
		callback string
		want     string
		wantErr  bool
	}{
		{"absent falls back to requested", "", "https://auth.wendy.sh/realms/acme", false},
		{"same realm", "https://auth.wendy.sh/realms/acme", "https://auth.wendy.sh/realms/acme", false},
		{"switched realm same host", "https://auth.wendy.sh/realms/beta", "https://auth.wendy.sh/realms/beta", false},
		{"trailing slash trimmed", "https://auth.wendy.sh/realms/beta/", "https://auth.wendy.sh/realms/beta", false},
		{"different host refused", "https://evil.example.com/realms/acme", "", true},
		{"different scheme refused", "http://auth.wendy.sh/realms/acme", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := effectiveLoginIssuer(requested, "", tc.callback)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q / %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestEffectiveLoginIssuerRealmLess(t *testing.T) {
	const base = "https://auth.wendy.dev"
	for _, tc := range []struct {
		name     string
		callback string
		want     string
		wantErr  bool
	}{
		{"selected realm", base + "/realms/acme", base + "/realms/acme", false},
		{"issuer required", "", "", true},
		{"system rejected", base + "/realms/system", "", true},
		{"extra path rejected", base + "/realms/acme/authorize", "", true},
		{"encoded realm rejected", base + "/realms/%61cme", "", true},
		{"query rejected", base + "/realms/acme?x=1", "", true},
		{"other authority rejected", "https://evil.example/realms/acme", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := effectiveLoginIssuer("", base, tc.callback)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q / %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestCanonicalAuthorizationBase(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"https://auth.wendy.dev/", "https://auth.wendy.dev", false},
		{"http://127.0.0.1:8080/", "http://127.0.0.1:8080", false},
		{"http://auth.wendy.dev", "", true},
		{"https://auth.wendy.dev/realms/acme", "", true},
		{"https://user@auth.wendy.dev", "", true},
	} {
		got, err := canonicalAuthorizationBase(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("canonicalAuthorizationBase(%q) = %q, want error", tc.raw, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("canonicalAuthorizationBase(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
}

func TestSameIssuerOrigin(t *testing.T) {
	if !sameIssuerOrigin("https://auth.wendy.sh/realms/a", "https://auth.wendy.sh/realms/b") {
		t.Fatal("same host+scheme, different path should match")
	}
	if sameIssuerOrigin("https://auth.wendy.sh/realms/a", "https://other.wendy.sh/realms/a") {
		t.Fatal("different host must not match")
	}
	if sameIssuerOrigin("not a url", "https://auth.wendy.sh") {
		t.Fatal("unparseable issuer must not match")
	}
}

// A second login that yields a different org (a different realm issuer) adds a
// context rather than replacing the first login's `default`.
