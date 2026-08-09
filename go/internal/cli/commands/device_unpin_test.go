package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/devicepin"
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
