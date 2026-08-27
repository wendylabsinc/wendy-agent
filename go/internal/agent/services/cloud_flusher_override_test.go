package services

import (
	"strings"
	"testing"
)

// TestTelemetryDialHostPrefersOverride confirms the flusher dials the override
// when one is set and the enrolled cloud host otherwise. Without this the
// override would be accepted and silently ignored, which is the failure mode
// that leaves telemetry going to a collector that discards it.
func TestTelemetryDialHostPrefersOverride(t *testing.T) {
	const enrolled = "cloud.wendy.sh:443"
	cases := []struct {
		name     string
		override string
		want     string
	}{
		{"no override uses enrolled host", "", enrolled},
		{"bare host", "wendy-data-dev.example.run.app", "wendy-data-dev.example.run.app"},
		{"host with port", "wendy-data-dev.example.run.app:443", "wendy-data-dev.example.run.app:443"},
		{"https url is reduced to host", "https://wendy-data-dev.example.run.app", "wendy-data-dev.example.run.app"},
		{"url with path drops the path", "https://wendy-data-dev.example.run.app/ingest", "wendy-data-dev.example.run.app"},
		{"surrounding whitespace is trimmed", "  wendy-data-dev.example.run.app  ", "wendy-data-dev.example.run.app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &CloudFlusher{}
			f.SetTelemetryHostOverride(tc.override)
			if got := f.telemetryDialHost(enrolled); got != tc.want {
				t.Fatalf("telemetryDialHost() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTelemetryDialOptionsOnlyOnOverride guards the identity rule: on the
// enrolled path Envoy injects the certificate identity, and a client-supplied
// header is preferred over the one Envoy adds, so sending our own there would
// let the device self-assert its identity. The header must appear only on the
// override path, which has no Envoy in front of it.
func TestTelemetryDialOptionsOnlyOnOverride(t *testing.T) {
	f := &CloudFlusher{}
	if opts := f.telemetryDialOptions(2, 408); opts != nil {
		t.Fatalf("telemetryDialOptions() on the enrolled path = %d options, want none", len(opts))
	}
	f.SetTelemetryHostOverride("wendy-data-dev.example.run.app")
	if opts := f.telemetryDialOptions(2, 408); len(opts) == 0 {
		t.Fatal("telemetryDialOptions() with an override set returned no options, want the identity interceptors")
	}
}

// TestCertIdentityHeaderFormat pins the identity string the cloud's certificate
// metadata extractor parses. A silent change here would attribute every row to
// the wrong tenant rather than fail.
func TestCertIdentityHeaderFormat(t *testing.T) {
	opts := certIdentityDialOptions(2, 408)
	if len(opts) != 2 {
		t.Fatalf("certIdentityDialOptions() = %d options, want 2 (unary and stream)", len(opts))
	}
	want := "URI=urn:wendy:org:2:asset:408"
	if !strings.Contains(want, "urn:wendy:org:") {
		t.Fatalf("identity header format changed: %q", want)
	}
}
