package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/devicepin"
	"github.com/wendylabsinc/wendy/go/internal/shared/discoverycache"
)

// seedSPKIPin writes a known_devices.json entry for key directly into the
// wendy config directory (set up by writePinTestConfig beforehand), without
// exercising CheckAndUpdate — that requires a live certificate. It mirrors
// devicepin.Store's on-disk format exactly, so a discrepancy here would be a
// bug in the test fixture, not in the store.
func seedSPKIPin(t *testing.T, key string) {
	t.Helper()
	dir, err := config.ConfigDir()
	if err != nil {
		t.Fatalf("config dir: %v", err)
	}
	devices := map[string]devicepin.PinnedDevice{
		key: {SPKIFingerprint: "sha256:deadbeef", DisplayName: "thor", LastSeen: "2026-01-01T00:00:00Z"},
	}
	data, err := json.Marshal(devices)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "known_devices.json"), data, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
}

// readSPKIPins loads known_devices.json back from the config dir for
// assertions. A missing file (never written, or the store's own flush
// deleted it — it never does, but nothing guarantees that) reads as no pins.
func readSPKIPins(t *testing.T) map[string]devicepin.PinnedDevice {
	t.Helper()
	dir, err := config.ConfigDir()
	if err != nil {
		t.Fatalf("config dir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "known_devices.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read known_devices.json: %v", err)
	}
	var devices map[string]devicepin.PinnedDevice
	if err := json.Unmarshal(data, &devices); err != nil {
		t.Fatalf("unmarshal known_devices.json: %v", err)
	}
	return devices
}

// TestDeviceUnpinClearsBothStores is the test the whole command exists for.
// Every identity refusal in enforceDeviceIdentity and
// challengeUnprovisionedDevice tells the user to run `wendy device unpin
// <host>`; if that only cleared one of the two independent pin stores, the
// same refusal would fire again on the very next connection and the command
// would look broken.
func TestDeviceUnpinClearsBothStores(t *testing.T) {
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
	})
	seedSPKIPin(t, "urn:wendy:org:7:asset:42")

	cmd := newDeviceUnpinCmd()
	if err := cmd.RunE(cmd, []string{"thor.local"}); err != nil {
		t.Fatalf("unpin: %v", err)
	}

	if pin, ok := readPins()["thor"]; ok {
		t.Errorf("config pin survived unpin: %+v", pin)
	}
	if _, ok := readSPKIPins(t)["urn:wendy:org:7:asset:42"]; ok {
		t.Error("SPKI pin survived unpin: the identity refusal would fire again on the next connect")
	}
}

// TestDeviceUnpinClearsAPinFoundUnderAnAlias is the dead end this closes.
//
// `wendy cloud discover` seeds pins under the name the cloud roster carries
// (the asset name, which is the discovery cache's display name), while dials
// name the device by its mDNS hostname. lookupPin already reconciles the two —
// so a dial to "wendyos-calm-zinnia" is governed, and refused, by the pin filed
// under "calm-zinnia" — but unpin cleared only the key it was handed. The
// refusal named a key nothing was filed under, unpinning it removed nothing,
// and the very next dial refused identically. Forever.
func TestDeviceUnpinClearsAPinFoundUnderAnAlias(t *testing.T) {
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"calm-zinnia": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42", Source: config.PinSourceCloud},
	})
	setPinCache(t, discoverycache.Entry{
		ID:          "dev-1",
		DisplayName: "calm-zinnia",
		Hostname:    "wendyos-calm-zinnia.local",
	})
	seedSPKIPin(t, "urn:wendy:org:7:asset:42")

	cmd := newDeviceUnpinCmd()
	if err := cmd.RunE(cmd, []string{"wendyos-calm-zinnia.local"}); err != nil {
		t.Fatalf("unpin: %v", err)
	}

	if pin, ok := readPins()["calm-zinnia"]; ok {
		t.Errorf("the pin that governs this host survived unpinning it by the name the refusal named (%+v); the next dial refuses identically and there is no way out", pin)
	}
	if _, ok := readSPKIPins(t)["urn:wendy:org:7:asset:42"]; ok {
		t.Error("SPKI pin survived an alias-keyed unpin")
	}
}

// TestDeviceUnpinClearsSPKIPinWithNoConfigPin covers the store that gets
// written where no config pin ever does. getAgentVersionAtAddress runs the dial
// ladder with the SPKI store for every device the mDNS prober enumerates, so
// `wendy device list` alone SPKI-pins the whole LAN with no config pin behind
// any of it. When one of those certificates is reissued, the old unpin declined
// to touch the SPKI store at all (it required an asset-bearing config pin to
// derive the key from), and recovery meant hand-editing known_devices.json.
//
// The discovery cache is what closes the gap: it records the asset and org the
// device advertised, which is exactly the pair the store is keyed by.
func TestDeviceUnpinClearsSPKIPinWithNoConfigPin(t *testing.T) {
	writePinTestConfig(t, nil)
	setPinCache(t, discoverycache.Entry{
		ID:          "dev-1",
		DisplayName: "Thor",
		Hostname:    "wendyos-thor.local",
		AssetID:     42,
		OrgID:       7,
	})
	seedSPKIPin(t, "urn:wendy:org:7:asset:42")

	cmd := newDeviceUnpinCmd()
	if err := cmd.RunE(cmd, []string{"wendyos-thor.local"}); err != nil {
		t.Fatalf("unpin: %v", err)
	}

	if _, ok := readSPKIPins(t)["urn:wendy:org:7:asset:42"]; ok {
		t.Error("SPKI pin survived unpin for a host with no config pin: the refusal it causes has no escape hatch but a text editor")
	}
}

// TestClearDevicePinForRepinClearsSPKIStore holds set-default to the claim
// pki/README.md already makes for it — that naming a device has "the same
// clearing effect" as unpin. Leaving the SPKI half behind made that false for
// the one refusal that has no other way out: set-default would clear the config
// pin, reconnect, and be rejected again by the pin store it never touched.
func TestClearDevicePinForRepinClearsSPKIStore(t *testing.T) {
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
	})
	setPinCache(t) // empty cache: exactly the single-key legacy shape
	seedSPKIPin(t, "urn:wendy:org:7:asset:42")

	clearDevicePinForRepin("wendy-thor.local")

	if pin, ok := readPins()["wendy-thor"]; ok {
		t.Errorf("config pin survived clearDevicePinForRepin: %+v", pin)
	}
	if _, ok := readSPKIPins(t)["urn:wendy:org:7:asset:42"]; ok {
		t.Error("SPKI pin survived clearDevicePinForRepin: set-default cannot re-pin a device whose key rotated, though the docs say it can")
	}
}

// TestDeviceUnpinUnknownHostIsNotAnError is the ambiguity ruling from the
// design: the command's contract is "this host ends up unpinned", which is
// already true for a host that was never pinned, so there is nothing to
// report as a failure.
func TestDeviceUnpinUnknownHostIsNotAnError(t *testing.T) {
	writePinTestConfig(t, nil)

	cmd := newDeviceUnpinCmd()
	if err := cmd.RunE(cmd, []string{"never-pinned.local"}); err != nil {
		t.Fatalf("unpinning an unpinned host: want nil, got %v", err)
	}
}

// TestDeviceUnpinAcceptsHostPortAddress covers the exact bug fixed in
// set-default during Task 6: a pin recorded for a `--device
// wendy-thor.local:50051` connection files under "wendy-thor.local" (the port
// stripped by pinKeyForAddr), so unpinning must strip the port the same way —
// passing the raw argument through would clear a key nothing was ever filed
// under and leave the real pin, and the refusal, in place.
func TestDeviceUnpinAcceptsHostPortAddress(t *testing.T) {
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
	})
	seedSPKIPin(t, "urn:wendy:org:7:asset:42")

	cmd := newDeviceUnpinCmd()
	if err := cmd.RunE(cmd, []string{"wendy-thor.local:50051"}); err != nil {
		t.Fatalf("unpin with an explicit port: %v", err)
	}

	if pin, ok := readPins()["wendy-thor"]; ok {
		t.Errorf("config pin survived unpin with an explicit port: %+v", pin)
	}
	if _, ok := readSPKIPins(t)["urn:wendy:org:7:asset:42"]; ok {
		t.Error("SPKI pin survived unpin with an explicit port")
	}
}

// TestDeviceUnpinLegacyPinHasNoSPKIKey covers a pin written before asset ids
// were recorded (config.DevicePin.AssetID == ""): there is no way to derive
// the SPKI store's URN key from it, since that key requires an asset id. Per
// the design's ambiguity ruling, this must clear the config pin and succeed
// rather than fail — there is no SPKI entry to fail on, either, since a
// legacy connection's certificate never had an asset id to pin in the first
// place.
func TestDeviceUnpinLegacyPinHasNoSPKIKey(t *testing.T) {
	readPins := writePinTestConfig(t, map[string]config.DevicePin{
		"wendy-thor": {OrgID: 7, CloudGRPC: "grpc.a.sh:443"},
	})

	cmd := newDeviceUnpinCmd()
	if err := cmd.RunE(cmd, []string{"wendy-thor.local"}); err != nil {
		t.Fatalf("unpin of a legacy (assetless) pin: want nil, got %v", err)
	}
	if pin, ok := readPins()["wendy-thor"]; ok {
		t.Errorf("config pin survived unpin: %+v", pin)
	}
}
