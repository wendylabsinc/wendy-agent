//go:build darwin || linux || windows

package commands

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// testCertPEM is selfSignedCertPEM with a caller-chosen expiry, so the expiry
// classification (valid / expiring soon / expired) can be exercised.
func testCertPEM(t *testing.T, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// runAuthStatus executes `auth status` with the given global --json setting and
// returns everything the command wrote to its out writer.
func runAuthStatus(t *testing.T, wantJSON bool) string {
	t.Helper()
	prev := jsonOutput
	jsonOutput = wantJSON
	t.Cleanup(func() { jsonOutput = prev })

	var buf bytes.Buffer
	cmd := newAuthStatusCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("auth status: %v", err)
	}
	return buf.String()
}

// statusConfig is a logged-in config with one long-lived certificate.
func statusConfig(t *testing.T, notAfter time.Time) *config.Config {
	t.Helper()
	return &config.Config{Auth: []config.AuthConfig{{
		CloudDashboard: "https://cloud.wendy.dev",
		CloudGRPC:      "prod.example.com:443",
		Certificates: []config.CertificateInfo{{
			OrganizationID: 7,
			UserID:         "user-abc",
			PemCertificate: testCertPEM(t, notAfter),
		}},
	}}}
}

// Regression for `wendy auth status --json`: it ignored the global --json flag
// and printed the human summary, so piping it into jq always failed. Note the
// root command also turns --json on implicitly for non-interactive stdout, so
// this hit every scripted invocation, not just explicit `--json`.
func TestAuthStatusJSONEmitsJSON(t *testing.T) {
	seedConfig(t, statusConfig(t, time.Now().Add(365*24*time.Hour)))

	out := runAuthStatus(t, true)

	var got struct {
		LoggedIn bool `json:"loggedIn"`
		Sessions []struct {
			Cloud          string `json:"cloud"`
			CloudGRPC      string `json:"cloudGrpc"`
			UserID         string `json:"userId"`
			OrganizationID int    `json:"organizationId"`
			Certificate    *struct {
				ExpiresAt    string `json:"expiresAt"`
				Expired      bool   `json:"expired"`
				ExpiringSoon bool   `json:"expiringSoon"`
			} `json:"certificate"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, out)
	}

	if !got.LoggedIn {
		t.Errorf("loggedIn = false, want true")
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got.Sessions))
	}
	s := got.Sessions[0]
	if s.Cloud != "https://cloud.wendy.dev" {
		t.Errorf("cloud = %q", s.Cloud)
	}
	if s.CloudGRPC != "prod.example.com:443" {
		t.Errorf("cloudGrpc = %q", s.CloudGRPC)
	}
	if s.UserID != "user-abc" {
		t.Errorf("userId = %q", s.UserID)
	}
	if s.OrganizationID != 7 {
		t.Errorf("organizationId = %d, want 7", s.OrganizationID)
	}
	if s.Certificate == nil {
		t.Fatal("certificate missing")
	}
	if s.Certificate.Expired || s.Certificate.ExpiringSoon {
		t.Errorf("a year-out cert reported expired=%v expiringSoon=%v", s.Certificate.Expired, s.Certificate.ExpiringSoon)
	}
	if _, err := time.Parse(time.RFC3339, s.Certificate.ExpiresAt); err != nil {
		t.Errorf("expiresAt %q is not RFC3339: %v", s.Certificate.ExpiresAt, err)
	}
}

// The logged-out path printed a human warning to stdout, which also broke jq.
func TestAuthStatusJSONWhenLoggedOut(t *testing.T) {
	seedConfig(t, &config.Config{})

	out := runAuthStatus(t, true)

	var got struct {
		LoggedIn bool  `json:"loggedIn"`
		Sessions []any `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("logged-out output is not valid JSON: %v\noutput:\n%s", err, out)
	}
	if got.LoggedIn {
		t.Errorf("loggedIn = true, want false")
	}
	if len(got.Sessions) != 0 {
		t.Errorf("got %d sessions, want 0", len(got.Sessions))
	}
	// sessions must be [] rather than null so `.sessions | length` works.
	if !strings.Contains(out, `"sessions": []`) {
		t.Errorf("want an empty array for sessions, got:\n%s", out)
	}
}

// An expired certificate must be flagged, not silently reported as fine.
func TestAuthStatusJSONFlagsExpiredCert(t *testing.T) {
	seedConfig(t, statusConfig(t, time.Now().Add(-24*time.Hour)))

	out := runAuthStatus(t, true)

	var got struct {
		Sessions []struct {
			Certificate struct {
				Expired bool `json:"expired"`
			} `json:"certificate"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, out)
	}
	if len(got.Sessions) != 1 || !got.Sessions[0].Certificate.Expired {
		t.Errorf("expired cert not flagged, got:\n%s", out)
	}
}

// A cert inside the 7-day window keeps the human warning's meaning in JSON.
func TestAuthStatusJSONFlagsExpiringSoonCert(t *testing.T) {
	seedConfig(t, statusConfig(t, time.Now().Add(48*time.Hour)))

	out := runAuthStatus(t, true)

	var got struct {
		Sessions []struct {
			Certificate struct {
				Expired      bool `json:"expired"`
				ExpiringSoon bool `json:"expiringSoon"`
			} `json:"certificate"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, out)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got.Sessions))
	}
	if got.Sessions[0].Certificate.Expired || !got.Sessions[0].Certificate.ExpiringSoon {
		t.Errorf("48h-out cert should be expiringSoon and not expired, got:\n%s", out)
	}
}

// Guard the human path: fixing --json must not change the default rendering.
func TestAuthStatusHumanOutputUnchanged(t *testing.T) {
	seedConfig(t, statusConfig(t, time.Now().Add(365*24*time.Hour)))

	out := runAuthStatus(t, false)

	if json.Valid([]byte(strings.TrimSpace(out))) {
		t.Errorf("human output should not be JSON, got:\n%s", out)
	}
	for _, want := range []string{"https://cloud.wendy.dev", "prod.example.com:443", "user-abc", "Org:  7"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q, got:\n%s", want, out)
		}
	}
}
