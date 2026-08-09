package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/discoverycache"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
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

// TestEnforceDeviceIdentityHardFails covers the approved policy change: a
// mismatch is refused identically in every mode, with no prompt.
//
// The arrangement is deliberately the one the old code let through — an
// interactive TTY, where a user could answer "yes" — because that answer is
// precisely what an attacker needs. "The human consented" is not evidence about
// which device answered, so it must not be a way through.
func TestEnforceDeviceIdentityHardFails(t *testing.T) {
	// Org 7 asset 43 against a pin for org 7 asset 42: both devices are
	// legitimately enrolled in the same organisation and cloud, so only the
	// asset id says the hostname now points at a different machine. It is the
	// case a user is least able to adjudicate from a one-line prompt.
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
	})
	obs := observedDeviceIdentity{mTLS: true, orgID: 7, assetID: "43"}

	origJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = origJSON })

	// Interactive, human output: the mode that used to prompt.
	stubInteractive(t)
	jsonOutput = false
	interactiveErr := enforceDeviceIdentity("wendy-thor.local", obs)
	if interactiveErr == nil {
		t.Fatal("interactive mismatch: want a refusal, got nil — a prompt is still deciding this")
	}
	if !strings.Contains(interactiveErr.Error(), "wendy device unpin wendy-thor.local") {
		t.Errorf("refusal %q does not name the command that resolves it", interactiveErr)
	}

	// A prompt answered "yes" would have re-pinned to asset 43. The original pin
	// surviving is the observable proof that nothing accepted the new identity —
	// an assertion on the error alone would also pass if the user had declined a
	// prompt that still ran.
	if pin := readPins()["wendy-thor"]; pin.AssetID != "42" {
		t.Errorf("pin after refusal = %+v, want the original asset 42 intact", pin)
	}

	// Same input, other two modes. Identical text is the point: a refusal that
	// reads differently depending on where it is printed invites the reader to
	// assume the interactive one is negotiable.
	jsonOutput = true
	jsonErr := enforceDeviceIdentity("wendy-thor.local", obs)
	jsonOutput = false
	stubNonInteractive(t)
	headlessErr := enforceDeviceIdentity("wendy-thor.local", obs)

	if jsonErr == nil || headlessErr == nil {
		t.Fatalf("mismatch must be refused in every mode: json=%v, non-interactive=%v", jsonErr, headlessErr)
	}
	if jsonErr.Error() != interactiveErr.Error() || headlessErr.Error() != interactiveErr.Error() {
		t.Errorf("refusal differs by mode:\n  interactive:     %v\n  json:            %v\n  non-interactive: %v", interactiveErr, jsonErr, headlessErr)
	}

	// The assertions above prove the OUTCOME no longer depends on the mode. This
	// proves the MECHANISM is gone: with no confirmation prompt anywhere in the
	// identity path, there is no mode-dependent branch left for one to hide in.
	// A function seam would only catch a prompt re-added THROUGH the seam; the
	// likely regression — someone reaching for tui.Confirm* directly, to be
	// helpful — is what this catches.
	src, readErr := os.ReadFile("device_pin.go")
	if readErr != nil {
		t.Fatalf("reading device_pin.go: %v", readErr)
	}
	if strings.Contains(string(src), "tui.Confirm") {
		t.Error("device_pin.go calls a tui.Confirm* prompt: the identity path must refuse, never ask — a MITM warning that can be dismissed gets dismissed")
	}
}

// TestEnforceDeviceIdentityAppliesToNonDefault guards the scope widening: the
// pin used to be checked only for the saved default device, so `--device
// <host>` — just as spoofable — went unchecked.
//
// It also pins down the correctness trap in that widening. The LKG stub asserts
// the key the DIAL was constrained by, and the refusal text carries the key the
// CHECK was made against; asserting both are "wendy-thor.local" is what proves
// a host cannot be pinned under one key and verified under another, which would
// disable enforcement while looking like it works.
func TestEnforceDeviceIdentityAppliesToNonDefault(t *testing.T) {
	stubNonInteractive(t)

	origFlag := deviceFlag
	deviceFlag = "wendy-thor.local"
	t.Cleanup(func() { deviceFlag = origFlag })

	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 9, CloudGRPC: "grpc.b.sh:443", AssetID: "42"},
	})
	// The whole point is that this device is NOT the default: with a default set
	// the old code would have checked it anyway and the test would prove nothing.
	if cfg, err := config.Load(); err != nil || cfg.DefaultDevice != "" {
		t.Fatalf("precondition: no default device may be set (default=%q, err=%v)", cfg.DefaultDevice, err)
	}

	// Reach the device over the cache fast path so no name resolution or real
	// dialling happens; the connection it hands back carries a certificate for
	// org 7, while the pin above says org 9.
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "wendy-thor.local", IP: "10.0.0.9", Port: 50052, MTLS: true,
	}, 0)
	var dialedPinKey string
	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry, pinKey string) (*grpcclient.AgentConnection, error, lkgOutcome) {
		dialedPinKey = pinKey
		return &grpcclient.AgentConnection{
			IsMTLS:   true,
			CertInfo: &config.CertificateInfo{OrganizationID: 7},
		}, nil, lkgConnected
	}
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, target dialTarget) (*grpcclient.AgentConnection, error, error) {
		t.Errorf("general ladder ran despite an LKG hit (addr %s)", target.Addr)
		return nil, nil, errors.New("unreachable")
	}
	origDiscover := discoverLANDevices
	discoverLANDevices = func(context.Context, time.Duration) ([]models.LANDevice, error) { return nil, nil }
	t.Cleanup(func() {
		dialAgentLKGFn, dialAgentLadderFn = origLKG, origLadder
		discoverLANDevices = origDiscover
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := connectToAgent(ctx, SuppressProvisioningHint(), SuppressUpdateCheck(), NonInteractive())
	if conn != nil {
		conn.Close()
		t.Fatal("connectToAgent returned a connection to a --device host whose certificate contradicts its pin")
	}
	if err == nil {
		t.Fatal("connectToAgent returned no error for a --device host whose certificate contradicts its pin")
	}
	if !strings.Contains(err.Error(), `device "wendy-thor.local" identity changed`) ||
		!strings.Contains(err.Error(), "wendy device unpin wendy-thor.local") {
		t.Fatalf("err = %v, want the identity refusal naming the device and the unpin escape hatch", err)
	}
	if dialedPinKey != "wendy-thor.local" {
		t.Errorf("dial was constrained by pin key %q, but the check was made against \"wendy-thor.local\"; a host pinned under one key and verified under another is unenforced", dialedPinKey)
	}
	if pin := readPins()["wendy-thor"]; pin.OrgID != 9 {
		t.Errorf("pin after refusal = %+v, want the original org 9 intact", pin)
	}
}

// TestPickerPinKeyMatchesDeviceFlagKey is the fourth path's half of the same
// agreement TestEnforceDeviceIdentityAppliesToNonDefault proves for --device:
// picking a device from the TUI must file its pin exactly where naming it on
// the command line looks for it.
//
// The DisplayName here is the shape a real WendyOS device advertises — a
// Title-Cased friendly name from the `displayname` TXT record, built from the
// device name, while the hostname is "wendyos-" + that name. Keying on it would
// produce two pins for one device, each path blind to the other's, which reads
// as enforcement while enforcing nothing.
func TestPickerPinKeyMatchesDeviceFlagKey(t *testing.T) {
	lan := &models.LANDevice{DisplayName: "Agx Orin", Hostname: "wendyos-agx-orin.local"}

	origFlag := deviceFlag
	deviceFlag = "wendyos-agx-orin.local"
	t.Cleanup(func() { deviceFlag = origFlag })
	_, flagKey, _, err := resolveDeviceAddress()
	if err != nil {
		t.Fatalf("resolveDeviceAddress: %v", err)
	}

	cfg := &config.Config{}
	cfg.SetDevicePin(pinKeyForLANDevice(lan), 7, "grpc.a.sh:443", "42")
	if _, ok := cfg.DevicePinFor(flagKey); !ok {
		t.Fatalf("a pin recorded for the picked device (key %q) is invisible to --device %q (key %q): pins under %v",
			pinKeyForLANDevice(lan), deviceFlag, flagKey, cfg.DevicePins)
	}

	// And the trap itself: the display name must not be what keys the pin.
	if strings.EqualFold(pinKeyForLANDevice(lan), lan.DisplayName) {
		t.Errorf("picker pin key = %q, want the hostname %q — the display name is not a transform of the hostname", pinKeyForLANDevice(lan), lan.Hostname)
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
