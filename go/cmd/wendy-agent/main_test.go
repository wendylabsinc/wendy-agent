package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
	"go.uber.org/zap/zaptest"
)

func TestBrokerURLForCloudHost(t *testing.T) {
	tests := []struct {
		name      string
		cloudHost string
		want      string
	}{
		{
			name:      "host without port uses broker port",
			cloudHost: "cloud.wendy.io",
			want:      "cloud.wendy.io:50052",
		},
		{
			name:      "cloud run endpoint keeps tls port",
			cloudHost: "wendy-cloud-services-114319063177.us-central1.run.app:443",
			want:      "wendy-cloud-services-114319063177.us-central1.run.app:443",
		},
		{
			name:      "local certificate port maps to local broker port",
			cloudHost: "localhost:50051",
			want:      "localhost:50052",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := brokerURLForCloudHost(tt.cloudHost); got != tt.want {
				t.Fatalf("brokerURLForCloudHost(%q) = %q, want %q", tt.cloudHost, got, tt.want)
			}
		})
	}
}

// TestEnvBytesRejectsBelowMinimum covers WENDY_DATA_MAX_BYTES=0. Zero passed
// the old "not negative" check, reached SetQuota, and was discarded there in
// favour of the built-in default, so a device ran on 50 GiB while its operator
// believed they had capped its episode store, and nothing was logged either
// way. Zero remains valid for the reserve, where it genuinely means "keep no
// headroom".
func TestEnvBytesRejectsBelowMinimum(t *testing.T) {
	logger := zaptest.NewLogger(t)
	const fallback = int64(4096)
	for _, tc := range []struct {
		name    string
		value   string
		minimum int64
		want    int64
	}{
		{"quota zero is rejected", "0", 1, fallback},
		{"quota negative is rejected", "-1", 1, fallback},
		{"quota positive is taken", "8192", 1, 8192},
		{"reserve zero is taken", "0", 0, 0},
		{"reserve negative is rejected", "-1", 0, fallback},
		{"unparseable is rejected", "lots", 1, fallback},
		{"whitespace is trimmed", "  8192  ", 1, 8192},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WENDY_TEST_BYTES", tc.value)
			if got := envBytes(logger, "WENDY_TEST_BYTES", fallback, tc.minimum); got != tc.want {
				t.Fatalf("envBytes(%q, minimum %d) = %d, want %d", tc.value, tc.minimum, got, tc.want)
			}
		})
	}
	// An unset variable is not a misconfiguration; it is the default.
	if got := envBytes(logger, "WENDY_TEST_BYTES_UNSET", fallback, 1); got != fallback {
		t.Fatalf("an unset variable gave %d, want the %d default", got, fallback)
	}
}

func TestCheckLocalAccessMode(t *testing.T) {
	dir := t.TempDir()
	absent, _ := trustedkeys.Open(filepath.Join(dir, "absent.json"))
	withOperator, _ := trustedkeys.Open(filepath.Join(dir, "operator.json"))
	withOwner, _ := trustedkeys.Open(filepath.Join(dir, "owner.json"))
	for _, s := range []struct {
		store *trustedkeys.Store
		role  trustedkeys.Role
	}{{withOperator, trustedkeys.RoleOperator}, {withOwner, trustedkeys.RoleOwner}} {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(k.Public())
		if _, err := s.store.Add(der, s.role, "", trustedkeys.AddedByInstall, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		mode    string
		store   *trustedkeys.Store
		wantErr bool
	}{
		{"", absent, false},
		{"auto", absent, false},
		{"required", absent, true},
		{"required", withOperator, true},
		{"required", withOwner, false},
		{"on", withOwner, true}, // unknown values refuse, never fall back open
	} {
		if err := checkLocalAccessMode(tc.mode, tc.store); (err != nil) != tc.wantErr {
			t.Errorf("checkLocalAccessMode(%q) = %v, wantErr %v", tc.mode, err, tc.wantErr)
		}
	}
}
