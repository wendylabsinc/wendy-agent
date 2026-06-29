package commands

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func ptr[T any](v T) *T { return &v }

func TestEvaluateVersionSkew(t *testing.T) {
	tests := []struct {
		name               string
		agent, cli, latest string
		want               checkStatus
		wantHintNonEmpty   bool
	}{
		{name: "equal, up to date", agent: "0.16.0", cli: "0.16.0", latest: "0.16.0", want: statusPass},
		{name: "agent behind cli", agent: "0.15.0", cli: "0.16.0", latest: "0.16.0", want: statusWarn, wantHintNonEmpty: true},
		{name: "cli behind agent", agent: "0.16.0", cli: "0.15.0", latest: "0.16.0", want: statusWarn, wantHintNonEmpty: true},
		{name: "agent behind latest", agent: "0.16.0", cli: "0.16.0", latest: "0.17.0", want: statusWarn, wantHintNonEmpty: true},
		{name: "dev agent ignored", agent: "dev", cli: "0.16.0", latest: "0.16.0", want: statusPass},
		{name: "dev cli ignored", agent: "0.16.0", cli: "dev", latest: "0.16.0", want: statusPass},
		{name: "no latest, equal", agent: "0.16.0", cli: "0.16.0", latest: "", want: statusPass},
		{name: "agent version missing", agent: "", cli: "0.16.0", latest: "0.16.0", want: statusSkip},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateVersionSkew(tc.agent, tc.cli, tc.latest)
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q (detail=%q)", got.Status, tc.want, got.Detail)
			}
			if tc.wantHintNonEmpty && got.Hint == "" {
				t.Errorf("expected a remediation hint, got none")
			}
		})
	}
}

func TestEvaluateDiskUsage(t *testing.T) {
	const tb = int64(1_000_000_000)
	tests := []struct {
		name        string
		used, total int64
		want        checkStatus
	}{
		{name: "healthy", used: 50 * tb, total: 100 * tb, want: statusPass},
		{name: "warn low", used: 95 * tb, total: 100 * tb, want: statusWarn},
		{name: "fail almost full", used: 99 * tb, total: 100 * tb, want: statusFail},
		{name: "unknown size", used: 0, total: 0, want: statusSkip},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateDiskUsage("/", tc.used, tc.total)
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q (detail=%q)", got.Status, tc.want, got.Detail)
			}
		})
	}
}

func TestEvaluateDisk(t *testing.T) {
	const tb = int64(1_000_000_000)
	resp := &agentpb.GetAgentVersionResponse{
		Partitions: []*agentpb.DiskPartition{
			{Mountpoint: "/", UsedBytes: 50 * tb, TotalBytes: 100 * tb},
			{Mountpoint: "/boot", UsedBytes: 1 * tb, TotalBytes: 2 * tb},
			{Mountpoint: "/data", UsedBytes: 99 * tb, TotalBytes: 100 * tb},
		},
	}
	got := evaluateDisk(resp)
	if len(got) != 2 {
		t.Fatalf("expected only / and /data evaluated, got %d results", len(got))
	}
	// /data is nearly full -> fail must be present.
	var sawFail bool
	for _, r := range got {
		if r.Name == "Disk /data" && r.Status == statusFail {
			sawFail = true
		}
		if r.Name == "Disk /boot" {
			t.Errorf("/boot should not be evaluated")
		}
	}
	if !sawFail {
		t.Errorf("expected /data to fail; got %+v", got)
	}

	// Fallback to legacy disk fields when no partitions.
	legacy := &agentpb.GetAgentVersionResponse{DiskUsedBytes: ptr(50 * tb), DiskTotalBytes: ptr(100 * tb)}
	if r := evaluateDisk(legacy); len(r) != 1 || r[0].Status != statusPass {
		t.Errorf("legacy disk fallback = %+v, want one pass", r)
	}

	// No data at all -> skip.
	if r := evaluateDisk(&agentpb.GetAgentVersionResponse{}); len(r) != 1 || r[0].Status != statusSkip {
		t.Errorf("no disk data = %+v, want one skip", r)
	}
}

func TestEvaluateGPU(t *testing.T) {
	tests := []struct {
		name string
		resp *agentpb.GetAgentVersionResponse
		want checkStatus
	}{
		{name: "no gpu", resp: &agentpb.GetAgentVersionResponse{HasGpu: ptr(false)}, want: statusPass},
		{
			name: "nvidia with cuda",
			resp: &agentpb.GetAgentVersionResponse{HasGpu: ptr(true), GpuVendor: ptr("nvidia"), CudaVersion: ptr("12.2"), JetpackVersion: ptr("6.2")},
			want: statusPass,
		},
		{
			name: "nvidia missing driver",
			resp: &agentpb.GetAgentVersionResponse{HasGpu: ptr(true), GpuVendor: ptr("nvidia")},
			want: statusWarn,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := evaluateGPU(tc.resp); got.Status != tc.want {
				t.Fatalf("status = %q, want %q (detail=%q)", got.Status, tc.want, got.Detail)
			}
		})
	}
}

func TestEvaluateAppHealth(t *testing.T) {
	containers := []*agentpb.AppContainer{
		{AppName: "good", RunningState: agentpb.AppRunningState_RUNNING, FailureCount: 0},
		{AppName: "flaky", RunningState: agentpb.AppRunningState_RUNNING, FailureCount: 2},
		{AppName: "crashloop", RunningState: agentpb.AppRunningState_STOPPED, FailureCount: 7},
	}
	got := evaluateAppHealth(containers)
	want := map[string]checkStatus{
		"App good":      statusPass,
		"App flaky":     statusWarn,
		"App crashloop": statusFail,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for _, r := range got {
		if want[r.Name] != r.Status {
			t.Errorf("%s: status = %q, want %q", r.Name, r.Status, want[r.Name])
		}
	}

	// No apps is a healthy pass.
	if r := evaluateAppHealth(nil); len(r) != 1 || r[0].Status != statusPass {
		t.Errorf("no apps = %+v, want one pass", r)
	}
}

func TestEvaluateMTLS(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)

	valid := &config.CertificateInfo{OrganizationID: 7, PemCertificate: makeCertPEM(t, now.AddDate(1, 0, 0))}
	soon := &config.CertificateInfo{OrganizationID: 7, PemCertificate: makeCertPEM(t, now.AddDate(0, 0, 5))}
	expired := &config.CertificateInfo{OrganizationID: 7, PemCertificate: makeCertPEM(t, now.AddDate(0, 0, -1))}

	tests := []struct {
		name string
		cert *config.CertificateInfo
		want checkStatus
	}{
		{name: "valid", cert: valid, want: statusPass},
		{name: "expiring soon", cert: soon, want: statusWarn},
		{name: "expired", cert: expired, want: statusFail},
		{name: "missing", cert: nil, want: statusFail},
		{name: "empty pem", cert: &config.CertificateInfo{OrganizationID: 7}, want: statusFail},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := evaluateMTLS(tc.cert, now, false); got.Status != tc.want {
				t.Fatalf("status = %q, want %q (detail=%q)", got.Status, tc.want, got.Detail)
			}
		})
	}

	// orgVerified annotates the detail.
	if got := evaluateMTLS(valid, now, true); got.Status != statusPass || !strings.Contains(got.Detail, "org verified") {
		t.Errorf("orgVerified detail = %q, want pass mentioning org verified", got.Detail)
	}
}

func TestSummarizeAndReportJSON(t *testing.T) {
	checks := []checkResult{
		{Name: "a", Status: statusPass},
		{Name: "b", Status: statusWarn},
		{Name: "c", Status: statusFail},
		{Name: "d", Status: statusSkip},
		{Name: "e", Status: statusPass},
	}
	s := summarize(checks)
	if s.Pass != 2 || s.Warn != 1 || s.Fail != 1 || s.Skip != 1 {
		t.Fatalf("summary = %+v", s)
	}

	report := doctorReport{Device: "wendy-thor.local:50051", Checks: checks, Summary: s}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round doctorReport
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.Device != report.Device || len(round.Checks) != len(checks) || round.Summary != s {
		t.Errorf("round-trip mismatch: %+v", round)
	}
}

// makeCertPEM creates a minimal self-signed certificate PEM with the given
// expiry, for exercising the mTLS evaluator.
func makeCertPEM(t *testing.T, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "doctor-test"},
		NotBefore:    notAfter.AddDate(-1, 0, 0),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
