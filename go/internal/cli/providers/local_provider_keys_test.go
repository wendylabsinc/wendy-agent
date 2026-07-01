package providers

import "testing"

// TestLocalProviderKeys verifies the local-target classification used by
// `wendy run`/`wendy discover` to hide on-machine run targets unless --all.
// The local container runtimes and the local machine are in; real external
// hardware (android phone, wendy-lite MCU) is out.
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
