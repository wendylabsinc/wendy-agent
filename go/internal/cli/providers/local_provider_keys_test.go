package providers

import "testing"

// TestShowLocalDevices verifies the WENDY_SHOW_LOCAL_DEVICES opt-in parsing:
// local run targets stay hidden unless the var is set to a truthy value.
func TestShowLocalDevices(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "yes", "Yes", "on", "ON", " true "}
	for _, v := range truthy {
		t.Setenv(ShowLocalDevicesEnv, v)
		if !ShowLocalDevices() {
			t.Errorf("ShowLocalDevices() = false for %q; want true", v)
		}
	}
	falsy := []string{"", "0", "false", "no", "off", "maybe", "2"}
	for _, v := range falsy {
		t.Setenv(ShowLocalDevicesEnv, v)
		if ShowLocalDevices() {
			t.Errorf("ShowLocalDevices() = true for %q; want false", v)
		}
	}
}

// TestLocalProviderKeys verifies the local-target classification used by the
// device picker and `wendy discover` to hide on-machine run targets unless
// ShowLocalDevices. The local container runtimes and the local machine are in;
// real external hardware (android phone, wendy-lite MCU) is out.
func TestLocalProviderKeys(t *testing.T) {
	want := map[string]bool{
		ProviderKeyLocal:          true,
		ProviderKeyDocker:         true,
		ProviderKeyAppleContainer: true,
	}

	got := map[string]bool{}
	for _, k := range LocalProviderKeys() {
		got[k] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("LocalProviderKeys() missing %q", k)
		}
	}
	for _, k := range []string{"android", "wendy-lite"} {
		if got[k] {
			t.Errorf("LocalProviderKeys() should not contain external hardware key %q", k)
		}
	}
}

func TestIsLocalProviderKey(t *testing.T) {
	local := []string{ProviderKeyLocal, ProviderKeyDocker, ProviderKeyAppleContainer}
	for _, k := range local {
		if !IsLocalProviderKey(k) {
			t.Errorf("IsLocalProviderKey(%q) = false; want true", k)
		}
	}
	for _, k := range []string{"android", "wendy-lite", "", "unknown"} {
		if IsLocalProviderKey(k) {
			t.Errorf("IsLocalProviderKey(%q) = true; want false", k)
		}
	}
}
