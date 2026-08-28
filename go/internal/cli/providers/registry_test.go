package providers

import "testing"

// TestIsNetworkAddressKey pins the predicate that keeps `wendy run` against a
// device hostname from paying for the provider availability probes. Both
// directions matter: a real provider key must still reach the registry lookup,
// and every address shape the device flag accepts must short-circuit.
func TestIsNetworkAddressKey(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		// Real provider keys — must NOT be treated as addresses, or the
		// providers would become unreachable via --device.
		{ProviderKeyDocker, false},
		{ProviderKeyAppleContainer, false},
		{ProviderKeyLocal, false},
		{"wendy-lite", false},
		{"", false},

		// Address shapes the device flag accepts.
		{"spark-3011.local", true},
		{"spark-3011.local:50051", true},
		{"192.168.0.24", true},
		{"192.168.0.24:50051", true},
		{"[fe80::1]", true},
		{"[fe80::1]:50051", true},
	} {
		if got := isNetworkAddressKey(tc.key); got != tc.want {
			t.Errorf("isNetworkAddressKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// TestProviderForKeyRejectsNetworkAddresses covers the same short-circuit at
// the exported boundary: an address never resolves to a provider, regardless
// of which providers happen to be available on the machine running the test.
func TestProviderForKeyRejectsNetworkAddresses(t *testing.T) {
	for _, key := range []string{"spark-3011.local", "192.168.0.24:50051", "[fe80::1]"} {
		if p := ProviderForKey(key); p != nil {
			t.Errorf("ProviderForKey(%q) = %v, want nil", key, p.Key())
		}
	}
}

// TestAllProvidersNeedsNoProbe asserts the static registration list is served
// without consulting availability — AllProviders is used by discovery paths
// that want every provider even when its toolchain is missing, so making it
// probe would reintroduce the startup cost this file exists to avoid.
func TestAllProvidersNeedsNoProbe(t *testing.T) {
	got := AllProviders()
	if len(got) != len(allProviders) {
		t.Fatalf("AllProviders() returned %d providers, want %d", len(got), len(allProviders))
	}
}

// TestAvailableProvidersIsStable checks the lazy probe is idempotent: repeated
// calls must return the same set rather than re-probing and appending.
func TestAvailableProvidersIsStable(t *testing.T) {
	first := AvailableProviders()
	second := AvailableProviders()
	if len(first) != len(second) {
		t.Fatalf("AvailableProviders() returned %d then %d providers; probe is not idempotent", len(first), len(second))
	}
	// LocalProvider always reports available, so the probe must find at least it.
	if len(first) == 0 {
		t.Fatal("AvailableProviders() returned nothing; LocalProvider is unconditionally available")
	}
}
