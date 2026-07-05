package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestOSAlreadyCurrent(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		nightly bool
		want    bool
	}{
		{"stable equal is current", "WendyOS-0.10.4", "0.10.4", false, true},
		{"stable newer available", "WendyOS-0.10.4", "0.12.0", false, false},
		{"stable device ahead is current", "WendyOS-0.12.0", "0.10.4", false, true},
		{"nightly equal is current", "WendyOS-0.12.0-nightly", "0.12.0-nightly", true, true},
		{"nightly different available", "WendyOS-0.12.0-nightly", "0.13.0-nightly", true, false},
		{"empty current not current", "", "0.10.4", false, false},
		{"empty latest not current", "WendyOS-0.10.4", "", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := osAlreadyCurrent(tc.current, tc.latest, tc.nightly); got != tc.want {
				t.Fatalf("osAlreadyCurrent(%q,%q,%v) = %v, want %v", tc.current, tc.latest, tc.nightly, got, tc.want)
			}
		})
	}
}

func TestDecideOSUpdate(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		latest      string
		nightly     bool
		assumeYes   bool
		interactive bool
		want        osUpdateAction
	}{
		{"already current", "WendyOS-0.10.4", "0.10.4", false, false, false, osActionAlreadyCurrent},
		{"newer with yes", "WendyOS-0.10.4", "0.12.0", false, true, false, osActionApply},
		{"newer with yes overrides tty", "WendyOS-0.10.4", "0.12.0", false, true, true, osActionApply},
		{"newer interactive prompts", "WendyOS-0.10.4", "0.12.0", false, false, true, osActionPrompt},
		{"newer noninteractive reports", "WendyOS-0.10.4", "0.12.0", false, false, false, osActionReportOnly},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decideOSUpdate(tc.current, tc.latest, tc.nightly, tc.assumeYes, tc.interactive)
			if got != tc.want {
				t.Fatalf("decideOSUpdate(%q,%q,nightly=%v,yes=%v,tty=%v) = %v, want %v",
					tc.current, tc.latest, tc.nightly, tc.assumeYes, tc.interactive, got, tc.want)
			}
		})
	}
}

func TestValidateOSUpdateIdentityAllowsWendyOSBeforeBackendCheck(t *testing.T) {
	osVersion := "WendyOS-0.10.4"
	cases := []*agentpb.GetAgentVersionResponse{
		{Os: "linux", OsVersion: &osVersion},
		{Os: "linux", OsVersion: &osVersion, Featureset: []string{"wendyos-update"}},
	}
	for _, resp := range cases {
		if err := validateOSUpdateIdentity(resp); err != nil {
			t.Fatalf("validateOSUpdateIdentity(%+v) error = %v, want nil", resp, err)
		}
	}
}

// Since #1136 the agent reports the /etc/os-release ID (e.g. "wendyos") in the
// Os field rather than "linux", so the identity check must not gate on
// Os == "linux"; the WendyOS-specific signals (version prefix / device type)
// are authoritative on their own.
func TestValidateOSUpdateIdentityAcceptsWendyOSReportedAsDistroID(t *testing.T) {
	strp := func(s string) *string { return &s }
	cases := []*agentpb.GetAgentVersionResponse{
		{Os: "wendyos", OsVersion: strp("WendyOS-0.10.4")},
		{Os: "edgeos", DeviceType: strp("jetson-orin-nano")},
	}
	for _, resp := range cases {
		if err := validateOSUpdateIdentity(resp); err != nil {
			t.Fatalf("validateOSUpdateIdentity(%+v) error = %v, want nil", resp, err)
		}
	}
}

func TestValidateOSUpdateTarget(t *testing.T) {
	strp := func(s string) *string { return &s }

	tests := []struct {
		name string
		resp *agentpb.GetAgentVersionResponse
		want string
	}{
		{
			name: "generic setup is not compatible",
			resp: &agentpb.GetAgentVersionResponse{Os: "darwin"},
			want: osUpdateUnsupportedMessage,
		},
		{
			name: "macOS version does not imply WendyOS",
			resp: &agentpb.GetAgentVersionResponse{Os: "darwin", OsVersion: strp("14.4.1")},
			want: osUpdateUnsupportedMessage,
		},
		{
			name: "linux host with agent is not WendyOS",
			resp: &agentpb.GetAgentVersionResponse{Os: "linux"},
			want: linuxOSUpdateUnsupportedMessage,
		},
		{
			name: "linux host with an update backend is still not WendyOS",
			resp: &agentpb.GetAgentVersionResponse{Os: "linux", Featureset: []string{"wendyos-update"}},
			want: linuxOSUpdateUnsupportedMessage,
		},
		{
			name: "linux OS version does not imply WendyOS",
			resp: &agentpb.GetAgentVersionResponse{Os: "linux", OsVersion: strp("22.04")},
			want: linuxOSUpdateUnsupportedMessage,
		},
		{
			name: "WendyOS without an update backend is unsupported",
			resp: &agentpb.GetAgentVersionResponse{Os: "linux", OsVersion: strp("WendyOS-0.10.4")},
			want: wendyOSMissingUpdaterMessage,
		},
		{
			name: "WendyOS version with wendyos-update is supported",
			resp: &agentpb.GetAgentVersionResponse{Os: "linux", OsVersion: strp("WendyOS-0.10.4"), Featureset: []string{"wendyos-update"}},
		},
		{
			name: "WendyOS device type with wendyos-update is supported",
			resp: &agentpb.GetAgentVersionResponse{Os: "linux", DeviceType: strp("raspberry-pi-5"), Featureset: []string{"wendyos-update"}},
		},
		{
			name: "WendyOS reported as a distro id (post-#1136) is supported",
			resp: &agentpb.GetAgentVersionResponse{Os: "wendyos", OsVersion: strp("WendyOS-0.10.4"), Featureset: []string{"wendyos-update"}},
		},
		{
			name: "WendyOS distro id with device type is supported",
			resp: &agentpb.GetAgentVersionResponse{Os: "edgeos", DeviceType: strp("jetson-orin-nano"), Featureset: []string{"wendyos-update"}},
		},
		{
			name: "generic distro id host still gets the Linux guidance",
			resp: &agentpb.GetAgentVersionResponse{Os: "ubuntu"},
			want: linuxOSUpdateUnsupportedMessage,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOSUpdateTarget(tc.resp)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validateOSUpdateTarget() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateOSUpdateTarget() error = nil, want %q", tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("validateOSUpdateTarget() error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

func TestHasOTABackend(t *testing.T) {
	tests := []struct {
		name string
		resp *agentpb.GetAgentVersionResponse
		want bool
	}{
		{
			name: "wendyos-update only (e.g. Jetson Orin Nano)",
			resp: &agentpb.GetAgentVersionResponse{Featureset: []string{"gpu", "wendyos-update", "os-healthcheck"}},
			want: true,
		},
		{
			// Regression: a legacy mender-only featureset (old CLI talking to a
			// stale agent) must not be treated as an OTA-capable backend.
			name: "mender only",
			resp: &agentpb.GetAgentVersionResponse{Featureset: []string{"mender"}},
			want: false,
		},
		{
			name: "wendyos-update alongside an unrelated legacy entry",
			resp: &agentpb.GetAgentVersionResponse{Featureset: []string{"wendyos-update", "mender"}},
			want: true,
		},
		{
			name: "no update backend",
			resp: &agentpb.GetAgentVersionResponse{Featureset: []string{"gpu", "audio"}},
			want: false,
		},
		{
			name: "empty featureset",
			resp: &agentpb.GetAgentVersionResponse{},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasOTABackend(tc.resp); got != tc.want {
				t.Fatalf("hasOTABackend() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOSUpdateStackMismatch(t *testing.T) {
	tests := []struct {
		name        string
		features    []string
		artifactURL string
		wantErr     bool
		wantSubstr  string
	}{
		{
			name:        "wendy artifact on a device that predates the wendyos-update stack requires a reflash",
			features:    []string{"os-healthcheck"},
			artifactURL: "https://storage.googleapis.com/img/wendyos-image.rootfs.wendy",
			wantErr:     true,
			wantSubstr:  "reflash",
		},
		{
			name:        "wendy artifact on a wendyos-update device is fine",
			features:    []string{"wendyos-update"},
			artifactURL: "https://storage.googleapis.com/img/wendyos-image.rootfs.wendy",
		},
		{
			name:        "unknown artifact extension is not constrained",
			features:    []string{"os-healthcheck"},
			artifactURL: "https://example.com/custom-artifact",
		},
		{
			name:        "device without advertised backends is left to the agent",
			features:    nil,
			artifactURL: "https://storage.googleapis.com/img/wendyos-image.rootfs.wendy",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &agentpb.GetAgentVersionResponse{Featureset: tc.features}
			err := osUpdateStackMismatch(resp, tc.artifactURL)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("osUpdateStackMismatch() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("osUpdateStackMismatch() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q should contain %q", err, tc.wantSubstr)
			}
		})
	}
}

func TestProgressLabel(t *testing.T) {
	tests := []struct {
		phase   string
		percent int32
		want    string
	}{
		{"installing", 42, "Installing update (42%)"},
		{"installing", 0, "Installing update..."},
		{"downloading", 0, "Downloading update..."},
		{"finalizing", 100, "Finalizing (100%)"},
		{"", 0, "Updating WendyOS..."},
	}
	for _, tc := range tests {
		if got := progressLabel(tc.phase, tc.percent); got != tc.want {
			t.Errorf("progressLabel(%q,%d) = %q, want %q", tc.phase, tc.percent, got, tc.want)
		}
	}
}

func TestFormatOSUpdateStatus(t *testing.T) {
	tests := []struct {
		name         string
		resp         *agentpb.GetOSUpdateStatusResponse
		wantContains []string
	}{
		{
			name:         "no record",
			resp:         &agentpb.GetOSUpdateStatusResponse{HasResult: false},
			wantContains: []string{"No OS update"},
		},
		{
			name: "commit failed shows the captured reason",
			resp: &agentpb.GetOSUpdateStatusResponse{
				HasResult:    true,
				Outcome:      agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMIT_FAILED,
				OldOsVersion: "WendyOS-0.10.4",
				NewOsVersion: "WendyOS-0.11.0",
				Note:         "wendyos-update commit failed: exit status 1 (tegra: ESRT capsule not staged)",
			},
			wantContains: []string{"commit", "ESRT capsule not staged", "WendyOS-0.11.0"},
		},
		{
			name: "rolled back lists failed services",
			resp: &agentpb.GetOSUpdateStatusResponse{
				HasResult: true,
				Outcome:   agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLED_BACK,
				Services: []*agentpb.GetOSUpdateStatusResponse_ServiceResult{
					{Unit: "avahi-daemon.service", Status: agentpb.GetOSUpdateStatusResponse_ServiceResult_STATUS_FAILED, Reason: "timed out"},
				},
			},
			wantContains: []string{"rolled back", "avahi-daemon.service", "timed out"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := formatOSUpdateStatus(tc.resp)
			for _, want := range tc.wantContains {
				if !strings.Contains(msg, want) {
					t.Errorf("formatOSUpdateStatus() = %q, missing %q", msg, want)
				}
			}
		})
	}
}

func TestResolveArtifactPath(t *testing.T) {
	t.Run("direct file is returned regardless of extension", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "update.wendy")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolveArtifactPath(f)
		if err != nil {
			t.Fatalf("resolveArtifactPath(%q) error = %v", f, err)
		}
		if got != f {
			t.Fatalf("resolveArtifactPath(%q) = %q, want %q", f, got, f)
		}
	})

	t.Run("directory search finds a .wendy artifact", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "image.wendy")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolveArtifactPath(dir)
		if err != nil {
			t.Fatalf("resolveArtifactPath(%q) error = %v", dir, err)
		}
		if got != f {
			t.Fatalf("resolveArtifactPath(%q) = %q, want %q", dir, got, f)
		}
	})

	t.Run("directory search does not find a .mender artifact", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "image.mender")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveArtifactPath(dir); err == nil {
			t.Fatalf("resolveArtifactPath(%q) error = nil, want error", dir)
		}
	})
}

func TestArtifactSuffix(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"wendy artifact", "https://storage.example.com/images/raspberry-pi-5/1.0/wendyos-image-x.rootfs.wendy", ".wendy"},
		{"wendy with query string", "https://storage.example.com/x.wendy?token=abc&exp=123", ".wendy"},
		{"unknown extension falls back to wendy", "https://storage.example.com/images/x.bin", ".wendy"},
		{"bare local path", "/tmp/update.wendy", ".wendy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := artifactSuffix(tc.url); got != tc.want {
				t.Fatalf("artifactSuffix(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestEvaluateOSUpdateOutcome(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-2 * time.Minute).Unix()
	stale := now.Add(-2 * time.Hour).Unix()

	committed := &agentpb.GetOSUpdateStatusResponse{
		HasResult:     true,
		Outcome:       agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMITTED,
		NewOsVersion:  "WendyOS-0.11.0",
		CreatedAtUnix: fresh,
		Services: []*agentpb.GetOSUpdateStatusResponse_ServiceResult{
			{Unit: "avahi-daemon.service", Status: agentpb.GetOSUpdateStatusResponse_ServiceResult_STATUS_HEALTHY},
		},
	}
	rolledBack := &agentpb.GetOSUpdateStatusResponse{
		HasResult:     true,
		Outcome:       agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLED_BACK,
		OldOsVersion:  "WendyOS-0.10.4",
		NewOsVersion:  "WendyOS-0.11.0",
		CreatedAtUnix: fresh,
		Services: []*agentpb.GetOSUpdateStatusResponse_ServiceResult{
			{Unit: "avahi-daemon.service", Status: agentpb.GetOSUpdateStatusResponse_ServiceResult_STATUS_FAILED, Reason: "timed out after 30s waiting for active"},
			{Unit: "containerd.service", Status: agentpb.GetOSUpdateStatusResponse_ServiceResult_STATUS_HEALTHY},
		},
	}
	rollbackFailed := &agentpb.GetOSUpdateStatusResponse{
		HasResult:     true,
		Outcome:       agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLBACK_FAILED,
		CreatedAtUnix: fresh,
		RollbackError: "wendyos-update reported nothing to roll back",
		Services: []*agentpb.GetOSUpdateStatusResponse_ServiceResult{
			{Unit: "avahi-daemon.service", Status: agentpb.GetOSUpdateStatusResponse_ServiceResult_STATUS_FAILED, Reason: "timed out"},
		},
	}
	commitFailed := &agentpb.GetOSUpdateStatusResponse{
		HasResult:     true,
		Outcome:       agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMIT_FAILED,
		CreatedAtUnix: fresh,
		Note:          "wendyos-update commit failed: exit status 1 (tegra: ESRT capsule not staged)",
	}
	// A delegated (wendyos-update health.d) rollback has no per-service results;
	// the reason is carried in Note and must still reach the user.
	delegatedRolledBack := &agentpb.GetOSUpdateStatusResponse{
		HasResult:     true,
		Outcome:       agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLED_BACK,
		OldOsVersion:  "WendyOS-0.10.4",
		CreatedAtUnix: fresh,
		Note:          "wendyos-update commit failed: exit status 1 (pending update is marked failed; run rollback)",
	}
	// A rollback-failed record can also carry the commit-rejection reason in
	// Note; it must not be dropped alongside RollbackError.
	rollbackFailedWithNote := &agentpb.GetOSUpdateStatusResponse{
		HasResult:     true,
		Outcome:       agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLBACK_FAILED,
		CreatedAtUnix: fresh,
		Note:          "wendyos-update commit failed: exit status 1 (pending update is marked failed; run rollback)",
		RollbackError: "wendyos-update reported nothing to roll back",
	}

	tests := []struct {
		name         string
		resp         *agentpb.GetOSUpdateStatusResponse
		rpcErr       error
		preVer       string
		postVer      string
		wantErr      bool
		wantContains []string
	}{
		{
			name:         "committed is verified success",
			resp:         committed,
			preVer:       "WendyOS-0.10.4",
			postVer:      "WendyOS-0.11.0",
			wantErr:      false,
			wantContains: []string{"verified"},
		},
		{
			name:    "committed for a version the device is not running is rejected",
			resp:    committed,
			preVer:  "WendyOS-0.10.4",
			postVer: "WendyOS-0.10.4",
			wantErr: true,
			wantContains: []string{
				"WendyOS-0.11.0",
				"WendyOS-0.10.4",
			},
		},
		{
			name:         "committed with unknown running version is trusted",
			resp:         committed,
			preVer:       "WendyOS-0.10.4",
			postVer:      "",
			wantErr:      false,
			wantContains: []string{"verified", "WendyOS-0.11.0"},
		},
		{
			name:    "rolled back reports failed services",
			resp:    rolledBack,
			preVer:  "WendyOS-0.10.4",
			postVer: "WendyOS-0.10.4",
			wantErr: true,
			wantContains: []string{
				"rolled back",
				"avahi-daemon.service",
				"timed out after 30s",
				"WendyOS-0.10.4",
			},
		},
		{
			name:    "delegated rollback surfaces the note when there are no service results",
			resp:    delegatedRolledBack,
			preVer:  "WendyOS-0.10.4",
			postVer: "WendyOS-0.10.4",
			wantErr: true,
			wantContains: []string{
				"rolled back",
				"WendyOS-0.10.4",
				"is marked failed",
			},
		},
		{
			name:         "rollback failed reports degraded state",
			resp:         rollbackFailed,
			preVer:       "WendyOS-0.10.4",
			postVer:      "WendyOS-0.11.0",
			wantErr:      true,
			wantContains: []string{"avahi-daemon.service", "nothing to roll back"},
		},
		{
			name:    "rollback failed surfaces the note alongside the rollback error",
			resp:    rollbackFailedWithNote,
			preVer:  "WendyOS-0.10.4",
			postVer: "WendyOS-0.11.0",
			wantErr: true,
			wantContains: []string{
				"is marked failed",
				"nothing to roll back",
			},
		},
		{
			name:         "commit failed surfaces the captured reason",
			resp:         commitFailed,
			preVer:       "WendyOS-0.10.4",
			postVer:      "WendyOS-0.11.0",
			wantErr:      true,
			wantContains: []string{"commit", "ESRT capsule not staged"},
		},
		{
			name:         "unimplemented with unchanged version warns of rollback",
			rpcErr:       status.Error(codes.Unimplemented, "unknown method"),
			preVer:       "WendyOS-0.10.4",
			postVer:      "WendyOS-0.10.4",
			wantErr:      true,
			wantContains: []string{"WendyOS-0.10.4"},
		},
		{
			name:         "unimplemented with changed version succeeds without verification",
			rpcErr:       status.Error(codes.Unimplemented, "unknown method"),
			preVer:       "WendyOS-0.10.4",
			postVer:      "WendyOS-0.11.0",
			wantErr:      false,
			wantContains: []string{"WendyOS-0.11.0"},
		},
		{
			name:    "no record with changed version succeeds without verification",
			resp:    &agentpb.GetOSUpdateStatusResponse{HasResult: false},
			preVer:  "WendyOS-0.10.4",
			postVer: "WendyOS-0.11.0",
			wantErr: false,
		},
		{
			name: "stale record falls back to version comparison",
			resp: &agentpb.GetOSUpdateStatusResponse{
				HasResult:     true,
				Outcome:       agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMITTED,
				CreatedAtUnix: stale,
			},
			preVer:  "WendyOS-0.10.4",
			postVer: "WendyOS-0.10.4",
			wantErr: true,
		},
		{
			name:         "unknown post version cannot verify but does not fail",
			resp:         &agentpb.GetOSUpdateStatusResponse{HasResult: false},
			preVer:       "WendyOS-0.10.4",
			postVer:      "",
			wantErr:      false,
			wantContains: []string{"could not be verified"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := evaluateOSUpdateOutcome(tc.resp, tc.rpcErr, tc.preVer, tc.postVer, now)
			if tc.wantErr && err == nil {
				t.Fatalf("error = nil, want non-nil; msg = %q", msg)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("error = %v, want nil; msg = %q", err, msg)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q missing %q", msg, want)
				}
			}
		})
	}
}
