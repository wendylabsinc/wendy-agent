package config

import (
	"os"
	"strings"
	"testing"
)

// TestLoginFlowEndToEnd mirrors what `wendy auth login` does: build an
// AuthConfig with plaintext secrets, AddAuth, Save — then prove a fresh
// process (new Load, cold memoization cache) gets working credentials while
// the JSON on disk holds none.
func TestLoginFlowEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WENDY_SECRET_STORE", "")
	store := newFakeStore()
	useFakeStore(t, store)
	origDefault := secretsPlatformDefault
	secretsPlatformDefault = true
	t.Cleanup(func() { secretsPlatformDefault = origDefault })

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.AddAuth(AuthConfig{
		CloudDashboard: "https://dash.wendy.com",
		CloudGRPC:      "grpc.wendy.com:443",
		APIKey:         "tok-login-e2e",
		Certificates: []CertificateInfo{{
			PemCertificate:      "-----BEGIN CERTIFICATE-----\npub",
			PemCertificateChain: "-----BEGIN CERTIFICATE-----\nchain",
			PemPrivateKey:       "-----BEGIN PRIVATE KEY-----\nsecret-key-e2e",
			OrganizationID:      7,
			UserID:              "user-e2e",
		}},
	})
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// "New process": cold cache, fresh Load.
	resetSecretCacheForTest()
	loaded, err := Load()
	if err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	auth := loaded.Auth[0]
	tok, err := auth.BearerToken()
	if err != nil || tok != "tok-login-e2e" {
		t.Fatalf("BearerToken = %q, %v", tok, err)
	}
	key, err := auth.Certificates[0].PrivateKeyPEM()
	if err != nil || !strings.Contains(key, "secret-key-e2e") {
		t.Fatalf("PrivateKeyPEM = %q, %v", key, err)
	}
	if auth.Certificates[0].PemCertificate == "" || auth.Certificates[0].PemCertificateChain == "" {
		t.Error("public cert material missing after round-trip")
	}

	path, _ := configPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	for _, secret := range []string{"tok-login-e2e", "secret-key-e2e"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("config.json contains secret %q", secret)
		}
	}

	// Logout: items deleted.
	DeleteStoredSecrets(loaded)
	if len(store.m) != 0 {
		t.Errorf("store holds %d items after logout cleanup", len(store.m))
	}
}
