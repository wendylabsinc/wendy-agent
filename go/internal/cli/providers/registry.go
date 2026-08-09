package providers

import (
	"context"
	"strings"
	"sync"
	"time"
)

var (
	allProviders       []DeviceProvider
	availableProviders []DeviceProvider
	availableOnce      sync.Once
	mu                 sync.RWMutex
)

func init() {
	allProviders = []DeviceProvider{
		&LocalProvider{},
		&AppleContainerProvider{},
		&DockerProvider{},
		&MicroWendyProvider{},
	}
}

// availabilityProbeTimeout bounds the whole availability sweep. The probes are
// `--version` calls that normally answer in well under a second; the cap is
// here so a wedged container runtime can't hang every CLI command that needs
// the provider list.
const availabilityProbeTimeout = 5 * time.Second

// ensureAvailable probes each registered provider exactly once, concurrently,
// and caches the providers that reported themselves available.
//
// The probe is lazy and parallel because it is neither free nor always needed:
// two of the four providers shell out (`docker --version`, `container
// --version`), ~88ms each on a warm macOS box. Running them sequentially at
// CLI startup put ~175ms in front of EVERY command — including the common
// `wendy run` against a device hostname, which never consults a provider at
// all. Probing on first use keeps that cost off commands that don't need it,
// and probing concurrently makes the sweep cost the slowest single probe
// rather than their sum.
func ensureAvailable() {
	availableOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), availabilityProbeTimeout)
		defer cancel()

		// Index-addressed so each goroutine owns its own slot and no lock is
		// needed during the sweep. Compacting in index order afterwards
		// preserves allProviders' registration order, which callers such as the
		// device picker rely on for stable output.
		probed := make([]DeviceProvider, len(allProviders))
		var wg sync.WaitGroup
		for i, p := range allProviders {
			wg.Go(func() {
				if p.IsAvailable(ctx) {
					probed[i] = p
				}
			})
		}
		wg.Wait()

		mu.Lock()
		defer mu.Unlock()
		availableProviders = nil
		for _, p := range probed {
			if p != nil {
				availableProviders = append(availableProviders, p)
			}
		}
	})
}

// AvailableProviders returns the providers whose toolchain is present on this
// machine, probing them on first call.
func AvailableProviders() []DeviceProvider {
	ensureAvailable()
	mu.RLock()
	defer mu.RUnlock()
	return availableProviders
}

// AllProviders returns all registered providers regardless of toolchain availability.
// Use this for device discovery where you want to find devices even if you can't
// build for them. This is static registration data, so it never triggers a probe.
func AllProviders() []DeviceProvider {
	mu.RLock()
	defer mu.RUnlock()
	return allProviders
}

// isNetworkAddressKey reports whether key is a network address rather than a
// possible provider key.
//
// Every built-in provider key is a short dotless token ("docker",
// "apple-container", "local", "wendy-lite"), so a key carrying a "." or a
// leading "[" is an address — a ".local" mDNS name, a hostname, an IPv4
// literal, or a bracketed IPv6 — and can never match one. This mirrors the
// same short-circuit resolveTargetInner applies before its findDeviceByID
// provider sweep.
func isNetworkAddressKey(key string) bool {
	return strings.Contains(key, ".") || strings.HasPrefix(key, "[")
}

// ProviderForKey returns the available provider registered under key, or nil.
func ProviderForKey(key string) DeviceProvider {
	// Rejecting addresses before ensureAvailable keeps `wendy run` against a
	// device hostname — the common case — from paying for the provider probes
	// at all.
	if isNetworkAddressKey(key) {
		return nil
	}
	ensureAvailable()
	mu.RLock()
	defer mu.RUnlock()
	for _, p := range availableProviders {
		if p.Key() == key {
			return p
		}
	}
	return nil
}
