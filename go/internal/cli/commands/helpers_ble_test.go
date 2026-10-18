package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestFindCertByOrgID(t *testing.T) {
	auth := []config.AuthConfig{
		{Certificates: []config.CertificateInfo{
			{OrganizationID: 5, PemCertificate: "cert5"},
		}},
		{Certificates: []config.CertificateInfo{
			{OrganizationID: 7, PemCertificate: "cert7a"},
			{OrganizationID: 7, PemCertificate: "cert7b"},
		}},
	}

	got := findCertByOrgID(auth, 7)
	if got == nil {
		t.Fatal("findCertByOrgID(7) = nil, want non-nil")
	}
	if got.PemCertificate != "cert7a" {
		t.Errorf("PemCertificate = %q, want %q", got.PemCertificate, "cert7a")
	}

	if got := findCertByOrgID(auth, 99); got != nil {
		t.Errorf("findCertByOrgID(99) = %v, want nil", got)
	}
}

func TestBLESelectionPreservesLANPinAgainstGATTHostname(t *testing.T) {
	setPinCache(t)
	setPinConfig(t, map[string]config.DevicePin{"trusted": {OrgID: 7, AssetID: "42"}})
	metadata := &models.BluetoothDevice{Hostname: "unpinned", Identity: "urn:wendy:org:7:asset:99"}
	selection := selectedBLEDevice(&models.DiscoveredDevice{LAN: &models.LANDevice{Hostname: "trusted.local"}, Bluetooth: metadata})
	if metadata.SelectedPinKey != "" {
		t.Fatal("selection mutated discovery metadata")
	}
	if blePinKey(selection.Bluetooth) != "trusted.local" {
		t.Fatalf("selected key=%q", blePinKey(selection.Bluetooth))
	}
	if _, err := bleExpectedIdentity(selection.Bluetooth); err == nil {
		t.Fatal("GATT hostname bypassed selected LAN pin")
	}
}

func TestBLEExpectedIdentityAllowsLegacyPinToReachHybridCertificateCheck(t *testing.T) {
	setPinCache(t)
	setPinConfig(t, map[string]config.DevicePin{"trusted": {OrgID: 7, AssetID: "42"}})
	principal := "spiffe://wendy.sh/tenant/12345678-1234-1234-1234-123456789abc/service/asset-42"
	device := &models.BluetoothDevice{Hostname: "trusted", Identity: principal}
	expected, err := bleExpectedIdentity(device)
	if err != nil || expected == nil || expected.OrgID != 7 || expected.EntityID != "42" {
		t.Fatalf("legacy expectation lost: %+v, %v", expected, err)
	}
	verified, err := certs.ParsePrincipal(principal)
	if err != nil {
		t.Fatal(err)
	}
	verified.OrgID = 7 // A hybrid certificate also carries its legacy URN.
	if err := verifyBLEMetadataIdentity(device, verified); err != nil {
		t.Fatal(err)
	}
	verified.Principal = strings.ReplaceAll(principal, "asset-42", "asset-99")
	if err := verifyBLEMetadataIdentity(device, verified); err == nil {
		t.Fatal("authenticated certificate did not match GATT identity")
	}
}

func TestBLEFirstContactStillRequiresAdvertisedDeviceIdentity(t *testing.T) {
	setPinCache(t)
	setPinConfig(t, nil)
	for _, raw := range []string{"", "garbage", "urn:wendy:org:7:user:1"} {
		if _, err := bleExpectedIdentity(&models.BluetoothDevice{Identity: raw}); err == nil {
			t.Fatalf("identity %q accepted", raw)
		}
	}
	device := &models.BluetoothDevice{Identity: "urn:wendy:org:7:asset:42"}
	got, err := bleExpectedIdentity(device)
	if err != nil || got == nil || got.EntityID != "42" {
		t.Fatalf("expectation=%+v, %v", got, err)
	}
}

func TestEnrolledBLERecoveryRetainsPinnedIdentityWhenLANIsUnreachable(t *testing.T) {
	setPinCache(t)
	setPinConfig(t, map[string]config.DevicePin{"trusted": {OrgID: 7, AssetID: "42"}})
	metadata := &models.BluetoothDevice{Hostname: "other", Enrolled: true, ProtocolVersion: 1, L2CAPPSM: 128, Identity: "urn:wendy:org:7:asset:42"}
	d := &models.DiscoveredDevice{LAN: &models.LANDevice{Hostname: "trusted.local"}, Bluetooth: metadata}
	if !canRecoverOverAuthenticatedBLE(d) {
		t.Fatal("authenticated recovery unavailable for unreachable LAN peer")
	}
	metadata.Identity = "urn:wendy:org:7:asset:99"
	if canRecoverOverAuthenticatedBLE(d) {
		t.Fatal("wrong device allowed as recovery candidate")
	}
	metadata.Identity = "urn:wendy:org:7:asset:42"
	metadata.Enrolled = false
	if canRecoverOverAuthenticatedBLE(d) {
		t.Fatal("unprovisioned BLE recovery allowed")
	}
}

func TestBLECredentialSelectionUsesTenantUUIDInsteadOfZeroLegacyOrg(t *testing.T) {
	tenantA := "11111111-1111-1111-1111-111111111111"
	tenantB := "22222222-2222-2222-2222-222222222222"
	candidates := []config.CertificateInfo{
		{PrincipalURI: "spiffe://wendy.sh/tenant/" + tenantA + "/operator/alice", PemCertificate: "first"},
		{PrincipalURI: "spiffe://wendy.sh/tenant/" + tenantB + "/operator/bob", PemCertificate: "second"},
	}
	got := preferredBLECredential(certs.WendyIdentity{TenantUUID: tenantB}, candidates)
	if got == nil || got.PemCertificate != "second" {
		t.Fatalf("credential=%+v", got)
	}
	if got := preferredBLECredential(certs.WendyIdentity{TenantUUID: "unknown"}, candidates); got != nil {
		t.Fatalf("unrelated credential matched: %+v", got)
	}
}

func recoveryBLEDevice(hostname, assetID string) models.BluetoothDevice {
	return models.BluetoothDevice{
		Hostname: hostname, Identity: "urn:wendy:org:7:asset:" + assetID,
		Enrolled: true, ProtocolVersion: 1, L2CAPPSM: 128, Capabilities: []string{"wifi"},
	}
}

func stubNamedBLEDiscovery(t *testing.T, devices ...models.BluetoothDevice) *int {
	t.Helper()
	calls := 0
	orig := discoverNamedBLEFn
	discoverNamedBLEFn = func(ctx context.Context) (<-chan []models.BluetoothDevice, error) {
		calls++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > namedBLERecoveryTimeout {
			t.Error("named BLE discovery has no bounded deadline")
		}
		ch := make(chan []models.BluetoothDevice, 1)
		ch <- devices
		close(ch)
		return ch, nil
	}
	t.Cleanup(func() { discoverNamedBLEFn = orig })
	return &calls
}

func TestNamedBLERecoveryOnlyScansForOptedInReachabilityFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   resolveConfig
		cause error
	}{
		{"not opted in", resolveConfig{}, errNoAuthenticatedEndpoint},
		{"enrollment", resolveConfig{includeBluetooth: true, enrollmentTrustRecovery: true}, errNoAuthenticatedEndpoint},
		{"identity refusal", resolveConfig{includeBluetooth: true}, refuseIdentity("wrong device")},
		{"TLS rejection", resolveConfig{includeBluetooth: true}, errors.New("remote error: tls: bad certificate")},
		{"unauthorized", resolveConfig{includeBluetooth: true}, errProvisionedAgentUnauthorized},
		{"unrelated error", resolveConfig{includeBluetooth: true}, errors.New("invalid command")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubNamedBLEDiscovery(t, recoveryBLEDevice("trusted", "42"))
			got, err := recoverNamedBLE(context.Background(), tc.cfg, "trusted.local", nil, tc.cause)
			if got != nil || err != nil || *calls != 0 {
				t.Fatalf("selection=%+v, err=%v, scans=%d", got, err, *calls)
			}
		})
	}
}

func TestNamedBLERecoverySelectsOnlyRequestedDeviceAndRetainsIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		candidate models.BluetoothDevice
		want      bool
		refused   bool
	}{
		{"hostname", recoveryBLEDevice("TRUSTED.local", "42"), true, false},
		{"known identity with renamed host", recoveryBLEDevice("renamed", "42"), true, false},
		{"different nearby device", recoveryBLEDevice("other", "99"), false, false},
		{"hostname with wrong identity", recoveryBLEDevice("trusted", "99"), false, true},
		{"incompatible metadata", func() models.BluetoothDevice {
			d := recoveryBLEDevice("trusted", "42")
			d.ProtocolVersion = 2
			return d
		}(), false, false},
		{"unenrolled metadata", func() models.BluetoothDevice { d := recoveryBLEDevice("trusted", "42"); d.Enrolled = false; return d }(), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setPinCache(t)
			pins := map[string]config.DevicePin{"trusted": {OrgID: 7, AssetID: "42"}}
			setPinConfig(t, pins)
			expected := expectedIdentityFor("trusted.local")
			calls := stubNamedBLEDiscovery(t, recoveryBLEDevice("unrelated", "10"), tc.candidate)
			got, err := recoverNamedBLE(context.Background(), resolveConfig{includeBluetooth: true}, "trusted.local", expected, errNoAuthenticatedEndpoint)
			if errors.Is(err, errDeviceIdentityRefused) != tc.refused || (err != nil && !tc.refused) || (got != nil) != tc.want || *calls != 1 {
				t.Fatalf("selection=%+v, err=%v, scans=%d", got, err, *calls)
			}
			if !tc.want {
				return
			}
			if got.Bluetooth.SelectedPinKey != "trusted.local" || got.Bluetooth.SelectedIdentity != expected.IdentityKey() {
				t.Fatalf("selected identity lost: %+v", got.Bluetooth)
			}
			// A subsequent config reload cannot weaken the handshake expectation.
			delete(pins, "trusted")
			got.Bluetooth.Identity = "urn:wendy:org:7:asset:99"
			if _, err := bleExpectedIdentity(got.Bluetooth); !errors.Is(err, errDeviceIdentityRefused) {
				t.Fatalf("selected pin disappeared with config reload: %v", err)
			}
		})
	}
}

func TestNamedBLERecoveryWithoutSavedIdentityRequiresExactHostname(t *testing.T) {
	setPinCache(t)
	setPinConfig(t, nil)
	stubNamedBLEDiscovery(t, recoveryBLEDevice("other", "42"))
	got, err := recoverNamedBLE(context.Background(), resolveConfig{includeBluetooth: true}, "trusted.local", nil, errNoAuthenticatedEndpoint)
	if got != nil || err != nil {
		t.Fatalf("selected arbitrary nearby agent: %+v, %v", got, err)
	}
}

func TestNamedBLERecoveryStopsWhenCallerCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	orig := discoverNamedBLEFn
	discoverNamedBLEFn = func(scanCtx context.Context) (<-chan []models.BluetoothDevice, error) {
		cancel()
		return make(chan []models.BluetoothDevice), nil
	}
	t.Cleanup(func() { discoverNamedBLEFn = orig })
	got, err := recoverNamedBLE(ctx, resolveConfig{includeBluetooth: true}, "trusted.local", nil, errNoAuthenticatedEndpoint)
	if got != nil || err != nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("selection=%+v, error=%v, caller=%v", got, err, ctx.Err())
	}
}

func TestResolveNamedOrDefaultDeviceRecoversOverBLEWithoutChangingDefault(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, optedIn := range []bool{false, true} {
			name := "default"
			if explicit {
				name = "explicit"
			}
			if optedIn {
				name += " with BLE"
			}
			t.Run(name, func(t *testing.T) {
				restoreDeviceGlobals(t)
				stubNonInteractive(t)
				setPinCache(t)
				t.Setenv("WENDY_AGENT_SOCKET", "")
				t.Setenv("WENDY_CONFIG_DIR", t.TempDir())
				setTempConfig(t, &config.Config{DefaultDevice: "trusted.local", DevicePins: map[string]config.DevicePin{"trusted": {OrgID: 7, AssetID: "42"}}})
				deviceFlag, deviceFlagFromEnv, jsonOutput = "", "", false
				if explicit {
					deviceFlag = "trusted.local"
				}
				origLadder, origDiscover := dialAgentLadderFn, discoverLANDevices
				dialAgentLadderFn = func(_ context.Context, target dialTarget) (*grpcclient.AgentConnection, error, error) {
					return nil, nil, errNoAuthenticatedEndpoint
				}
				discoverLANDevices = func(context.Context, time.Duration) ([]models.LANDevice, error) { return nil, nil }
				t.Cleanup(func() { dialAgentLadderFn, discoverLANDevices = origLadder, origDiscover })
				calls := stubNamedBLEDiscovery(t, recoveryBLEDevice("trusted", "42"))
				opts := []resolveOption{SuppressProvisioningHint(), SuppressUpdateCheck(), NonInteractive(), DisableSessionBroker()}
				if optedIn {
					opts = append(opts, IncludeBluetooth())
				}
				got, err := resolveTarget(context.Background(), opts...)
				if optedIn {
					if err != nil || got == nil || got.Bluetooth == nil || got.Bluetooth.SelectedPinKey != "trusted.local" || got.Bluetooth.SelectedIdentity != "urn:wendy:org:7:asset:42" || *calls != 1 {
						t.Fatalf("selection=%+v, err=%v, scans=%d", got, err, *calls)
					}
				} else if got != nil || !errors.Is(err, errNoAuthenticatedEndpoint) || *calls != 0 {
					t.Fatalf("selection=%+v, err=%v, scans=%d", got, err, *calls)
				}
				cfg, err := config.Load()
				if err != nil || cfg.DefaultDevice != "trusted.local" || cfg.DevicePins["trusted"].AssetID != "42" {
					t.Fatalf("saved default/pin changed: %+v, %v", cfg, err)
				}
			})
		}
	}
}
