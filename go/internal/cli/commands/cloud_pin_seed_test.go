package commands

import (
	"os"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// stubLoadConfigForPin points the loadConfigForPinFn seam at cfg for the
// duration of a test.
func stubLoadConfigForPin(t *testing.T, cfg *config.Config) {
	t.Helper()
	orig := loadConfigForPinFn
	loadConfigForPinFn = func() (*config.Config, error) { return cfg, nil }
	t.Cleanup(func() { loadConfigForPinFn = orig })
}

// TestSeedPinsFromCloudAssets covers seedPinsFromCloudAssets in isolation via
// the loadConfigForPinFn seam, so no test here touches the developer's real
// config file.
func TestSeedPinsFromCloudAssets(t *testing.T) {
	t.Run("writes a cloud-sourced pin per asset", func(t *testing.T) {
		cfg := &config.Config{}
		stubLoadConfigForPin(t, cfg)
		// config.Save is not seamed, so it still hits the real filesystem path
		// derived from HOME; point that at a scratch dir for this test.
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)

		assets := []*cloudpb.Asset{
			{Id: 42, Name: "calm-zinnia"},
		}
		if err := seedPinsFromCloudAssets(assets, 7, "grpc.a.sh:443"); err != nil {
			t.Fatalf("seedPinsFromCloudAssets: unexpected error: %v", err)
		}

		pin, ok := cfg.DevicePinFor("calm-zinnia")
		if !ok {
			t.Fatal("expected a pin for \"calm-zinnia\", found none")
		}
		want := config.DevicePin{OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42", Source: config.PinSourceCloud}
		if pin != want {
			t.Fatalf("pin = %+v, want %+v", pin, want)
		}

		// Must actually have persisted (config.Save called), not just mutated
		// the in-memory cfg the seam handed back.
		onDisk, err := config.Load()
		if err != nil {
			t.Fatalf("config.Load: %v", err)
		}
		if p, ok := onDisk.DevicePinFor("calm-zinnia"); !ok || p != want {
			t.Fatalf("on-disk pin = %+v (ok=%v), want %+v", p, ok, want)
		}
	})

	t.Run("overwrites a conflicting lan pin", func(t *testing.T) {
		cfg := &config.Config{
			DevicePins: map[string]config.DevicePin{
				"calm-zinnia": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "99", Source: config.PinSourceLAN},
			},
		}
		stubLoadConfigForPin(t, cfg)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)

		assets := []*cloudpb.Asset{
			{Id: 42, Name: "calm-zinnia"},
		}
		if err := seedPinsFromCloudAssets(assets, 7, "grpc.a.sh:443"); err != nil {
			t.Fatalf("seedPinsFromCloudAssets: unexpected error: %v", err)
		}

		pin, ok := cfg.DevicePinFor("calm-zinnia")
		if !ok {
			t.Fatal("expected a pin for \"calm-zinnia\", found none")
		}
		if pin.AssetID != "42" {
			t.Fatalf("AssetID = %q, want %q — the cloud roster must overwrite the conflicting LAN sighting (asset 99)", pin.AssetID, "42")
		}
		if pin.Source != config.PinSourceCloud {
			t.Fatalf("Source = %q, want %q — a cloud-fetched roster is authority, not a sighting, and must win outright", pin.Source, config.PinSourceCloud)
		}
	})

	t.Run("skips assets with no name", func(t *testing.T) {
		cfg := &config.Config{}
		stubLoadConfigForPin(t, cfg)

		// Point HOME at a path that cannot hold a ~/.wendy directory (a regular
		// file, not a directory), so that if seedPinsFromCloudAssets called
		// config.Save it would fail loudly. Every asset below is unnamed, so
		// "changed" must stay false and Save must never be attempted — proven
		// by this call succeeding despite the broken HOME.
		unwritableHome := t.TempDir() + "-not-a-directory"
		if err := os.WriteFile(unwritableHome, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("writing sentinel file: %v", err)
		}
		t.Setenv("HOME", unwritableHome)
		t.Setenv("USERPROFILE", unwritableHome)

		assets := []*cloudpb.Asset{
			{Id: 1, Name: ""},
			{Id: 2, Name: ""},
		}
		if err := seedPinsFromCloudAssets(assets, 7, "grpc.a.sh:443"); err != nil {
			t.Fatalf("seedPinsFromCloudAssets: unexpected error: %v (config.Save should never have been attempted)", err)
		}

		if len(cfg.DevicePins) != 0 {
			t.Fatalf("DevicePins = %+v, want empty — an unnamed asset must not be pinned under \"\"", cfg.DevicePins)
		}
		if _, ok := cfg.DevicePinFor(""); ok {
			t.Fatal("an unnamed asset was pinned under the empty-string key")
		}
	})
}
