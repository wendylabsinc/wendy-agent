package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// TestCloudGRPCForOrg maps the org carried by a verifying mTLS cert back to the
// cloud host of the auth session that issued it — the cloud half of the
// WDY-1149 (org, cloud) pin.
func TestCloudGRPCForOrg(t *testing.T) {
	cfg := &config.Config{Auth: []config.AuthConfig{
		{CloudGRPC: "grpc.a.sh:443", Certificates: []config.CertificateInfo{{OrganizationID: 7}}},
		{CloudGRPC: "grpc.b.sh:443", Certificates: []config.CertificateInfo{{OrganizationID: 9}}},
	}}

	if got := cloudGRPCForOrg(cfg, 9); got != "grpc.b.sh:443" {
		t.Errorf("org 9: got %q, want grpc.b.sh:443", got)
	}
	if got := cloudGRPCForOrg(cfg, 7); got != "grpc.a.sh:443" {
		t.Errorf("org 7: got %q, want grpc.a.sh:443", got)
	}
	if got := cloudGRPCForOrg(cfg, 42); got != "" {
		t.Errorf("unknown org: got %q, want empty", got)
	}
}

// writePinTestConfig points config.Load/Save at a temp HOME holding an auth
// session for org 7 on grpc.a.sh:443 plus the given pins, and returns a reader
// for the pins as they stand on disk.
func writePinTestConfig(t *testing.T, pins map[string]config.DevicePin) func() map[string]config.DevicePin {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// config.Load resolves ~/.wendy via os.UserHomeDir, which reads USERPROFILE
	// on Windows and HOME elsewhere.
	t.Setenv("USERPROFILE", home)

	cfg := &config.Config{
		Auth: []config.AuthConfig{
			{CloudGRPC: "grpc.a.sh:443", Certificates: []config.CertificateInfo{{OrganizationID: 7}}},
		},
		DevicePins: pins,
	}
	dir := filepath.Join(home, ".wendy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return func() map[string]config.DevicePin {
		t.Helper()
		got, loadErr := config.Load()
		if loadErr != nil {
			t.Fatalf("reload config: %v", loadErr)
		}
		return got.DevicePins
	}
}

// TestEnforceDeviceIdentityRejectsUnprovisionedPinnedDevice is the core of this
// change: a hostname we have previously seen enrolled now answers with no mTLS
// identity at all. That is a reflash, a factory reset, or something else
// squatting the name — never a silent "hint and continue".
func TestEnforceDeviceIdentityRejectsUnprovisionedPinnedDevice(t *testing.T) {
	stubNonInteractive(t)
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
	})

	err := enforceDeviceIdentity("wendy-thor.local", observedDeviceIdentity{})
	if err == nil {
		t.Fatal("pinned device answering unprovisioned: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "wendy-thor") {
		t.Errorf("error %q does not name the device", err)
	}
	// Refusing must not quietly discard the pin — the next connect has to make
	// the same challenge, not sail through as a first use.
	if pin, ok := readPins()["wendy-thor"]; !ok || pin.AssetID != "42" {
		t.Errorf("pin after refusal = %+v (present=%v), want the original pin intact", pin, ok)
	}
}

// TestEnforceDeviceIdentityAllowsUnpinnedUnprovisionedDevice keeps the normal
// out-of-the-box flow working: a device that was never enrolled has no pin, so
// connecting to it over plaintext is exactly what is supposed to happen.
func TestEnforceDeviceIdentityAllowsUnpinnedUnprovisionedDevice(t *testing.T) {
	stubNonInteractive(t)
	writePinTestConfig(t, nil)

	if err := enforceDeviceIdentity("wendy-thor.local", observedDeviceIdentity{}); err != nil {
		t.Fatalf("unpinned unprovisioned device: want nil, got %v", err)
	}
}

func TestEnforceDeviceIdentityRecordsAssetOnFirstUse(t *testing.T) {
	stubNonInteractive(t)
	readPins := writePinTestConfig(t, nil)

	err := enforceDeviceIdentity("wendy-thor.local", observedDeviceIdentity{mTLS: true, orgID: 7, assetID: "42"})
	if err != nil {
		t.Fatalf("first use: want nil, got %v", err)
	}
	pin, ok := readPins()["wendy-thor"]
	if !ok {
		t.Fatal("first use did not record a pin")
	}
	if pin.OrgID != 7 || pin.CloudGRPC != "grpc.a.sh:443" || pin.AssetID != "42" {
		t.Errorf("recorded pin = %+v, want org 7 / grpc.a.sh:443 / asset 42", pin)
	}
}

// TestEnforceDeviceIdentityRejectsDifferentAsset is the org-blind case the pin
// used to miss entirely: both devices are legitimately enrolled in org 7 via
// the same cloud, so only the asset id reveals that the hostname now points at
// a different machine.
func TestEnforceDeviceIdentityRejectsDifferentAsset(t *testing.T) {
	stubNonInteractive(t)
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
	})

	err := enforceDeviceIdentity("wendy-thor.local", observedDeviceIdentity{mTLS: true, orgID: 7, assetID: "43"})
	if err == nil {
		t.Fatal("different asset id at pinned hostname: want an error, got nil")
	}
	if pin := readPins()["wendy-thor"]; pin.AssetID != "42" {
		t.Errorf("pin after refusal = %+v, want the original asset 42 intact", pin)
	}
}

func TestEnforceDeviceIdentityAcceptsRotatedCertForSameDevice(t *testing.T) {
	stubNonInteractive(t)
	writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
	})

	if err := enforceDeviceIdentity("wendy-thor.local", observedDeviceIdentity{mTLS: true, orgID: 7, assetID: "42"}); err != nil {
		t.Fatalf("same org+cloud+asset (rotated cert): want nil, got %v", err)
	}
}

// TestEnforceDeviceIdentityBackfillsLegacyPin upgrades pins written before
// asset ids existed, without prompting: org and cloud already match, so there
// is nothing to challenge — but the asset must be recorded so the NEXT swap is
// caught.
func TestEnforceDeviceIdentityBackfillsLegacyPin(t *testing.T) {
	stubNonInteractive(t)
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443"},
	})

	if err := enforceDeviceIdentity("wendy-thor.local", observedDeviceIdentity{mTLS: true, orgID: 7, assetID: "42"}); err != nil {
		t.Fatalf("legacy pin backfill: want nil, got %v", err)
	}
	if pin := readPins()["wendy-thor"]; pin.AssetID != "42" {
		t.Errorf("pin after backfill = %+v, want asset 42 recorded", pin)
	}
}

// TestEnforceDeviceIdentityAcceptsAssetlessCert keeps older agents usable: a
// certificate with no asset identity proves nothing about which device it is,
// and absence of evidence must not read as a swap.
func TestEnforceDeviceIdentityAcceptsAssetlessCert(t *testing.T) {
	stubNonInteractive(t)
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
	})

	if err := enforceDeviceIdentity("wendy-thor.local", observedDeviceIdentity{mTLS: true, orgID: 7}); err != nil {
		t.Fatalf("cert without asset identity: want nil, got %v", err)
	}
	if pin := readPins()["wendy-thor"]; pin.AssetID != "42" {
		t.Errorf("pin = %+v, want asset 42 left untouched", pin)
	}
}

// TestClearDevicePinForRepin backs the escape hatch both refusal messages point
// at: `wendy device set-default <host>` names the device explicitly, which is
// the user asserting they mean this one — so it drops the stale pin and lets
// the connect that follows record a fresh one. Without this the advice is a
// dead end, because that command's own connect hits the same refusal.
func TestClearDevicePinForRepin(t *testing.T) {
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
		"wendy-orin": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "9"},
	})

	clearDevicePinForRepin("wendy-thor.local")

	pins := readPins()
	if _, ok := pins["wendy-thor"]; ok {
		t.Error("pin for the named device survived clearDevicePinForRepin")
	}
	if _, ok := pins["wendy-orin"]; !ok {
		t.Error("clearDevicePinForRepin dropped an unrelated device's pin")
	}
}

// TestEnforceDeviceIdentityRejectsDifferentOrg is the original WDY-1149
// behaviour, kept intact by the asset work.
func TestEnforceDeviceIdentityRejectsDifferentOrg(t *testing.T) {
	stubNonInteractive(t)
	writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 9, CloudGRPC: "grpc.b.sh:443", AssetID: "42"},
	})

	if err := enforceDeviceIdentity("wendy-thor.local", observedDeviceIdentity{mTLS: true, orgID: 7, assetID: "42"}); err == nil {
		t.Fatal("different org at pinned hostname: want an error, got nil")
	}
}
