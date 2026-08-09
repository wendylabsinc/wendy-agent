package commands

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/devicepin"
)

// dialTarget carries what the ladder needs to know about *who* it is dialing,
// not just where. Before this, the ladder took a bare address, so the identity
// the user asked for was gone by the time a certificate arrived — which is
// exactly what let a spoofed mDNS answer redirect a connection to another
// same-CA host.
type dialTarget struct {
	// PinKey is the name the user asked for (--device value, saved default, or
	// the picker's device name) — never the resolved IP, which changes on
	// ordinary DHCP churn and would train users to unpin reflexively. An empty
	// PinKey disables pin enforcement for this dial.
	PinKey string
	// PinnedKey is the key the governing pin is ACTUALLY filed under, which is
	// not always PinKey: lookupPin resolves a pin across every name the device
	// answers to, so dialling "wendyos-calm-zinnia" can be governed by a pin
	// recorded under the cloud roster's "calm-zinnia". Empty when no pin
	// governs this dial.
	//
	// It exists because a refusal has to name a key `wendy device unpin` can
	// act on. Naming the dialled key when an alias holds the pin sends the user
	// to a command that clears nothing, and the next dial refuses identically —
	// a permanent dead end dressed up as an escape hatch.
	PinnedKey string
	// Addr is the host:port actually dialed.
	Addr string
	// Expected constrains the peer certificate. Non-nil only when a pin (or a
	// cloud-seeded value) names a specific asset.
	Expected *certs.WendyIdentity
}

// refusalKey is the name a refusal for this dial must print: the key the pin is
// filed under when one governs, else the name the user asked for. Falling back
// to PinKey keeps a hand-built dialTarget (and any future caller that sets only
// PinKey) naming something rather than an empty string.
func (t dialTarget) refusalKey() string {
	if t.PinnedKey != "" {
		return t.PinnedKey
	}
	return t.PinKey
}

// loadConfigForPinFn is a seam over config.Load for tests.
var loadConfigForPinFn = config.Load

// plaintextConnectFn is a seam over grpcclient.Connect — the ladder's last,
// unauthenticated rung — so a test can prove that rung was never reached.
var plaintextConnectFn = grpcclient.Connect

// identityMismatchFn is a seam over the ladder's reading of a wrong-device
// rejection. The flag itself can only be set by a real TLS handshake — the
// VerifyConnection sink owns the unexported field — so seaming the ladder's
// CONSUMPTION of it is what puts the abort under test without a live ML-DSA
// peer. That abort matters on its own: once a cloud-seeded Expected can exist
// for a host with no config pin, it is the only thing standing between a wrong
// device and the plaintext rung.
var identityMismatchFn = (*grpcclient.AgentConnection).IdentityMismatch

// pinMismatchFn is the same seam for the SPKI store's rejection. It is seamed
// for the same reason: only a real handshake against a device whose key rotated
// can set the flag, so testing the ladder's REACTION to it — abort, no
// plaintext rung, a message naming `wendy device unpin` — needs the read
// stubbed rather than a live ML-DSA peer with a rotated keypair.
var pinMismatchFn = (*grpcclient.AgentConnection).PinMismatch

// newDialTarget resolves the pin for pinKey and returns a target constrained by
// it. Key resolution deliberately may read discovery-derived names: choosing
// the wrong key can only ever produce a mismatch — a stricter outcome — never
// a bypass, because the trust decision itself stays on the certificate.
func newDialTarget(pinKey, addr string) dialTarget {
	target := dialTarget{PinKey: pinKey, Addr: addr}
	pin, key, ok := governingPin(pinKey)
	if !ok {
		return target
	}
	target.PinnedKey = key
	if pin.AssetID != "" {
		target.Expected = &certs.WendyIdentity{OrgID: int32(pin.OrgID), EntityType: "asset", EntityID: pin.AssetID}
	}
	return target
}

// governingPin resolves the pin that governs pinKey across every name the
// device answers to, returning it and the key it is actually filed under. It is
// the single reader of pin state on the dial path, so the identity a dial
// enforces, the fact that it is pinned, and the key a refusal names can never
// disagree about which pin they are talking about.
func governingPin(pinKey string) (config.DevicePin, string, bool) {
	if pinKey == "" {
		return config.DevicePin{}, "", false
	}
	cfg, err := loadConfigForPinFn()
	if err != nil {
		return config.DevicePin{}, "", false
	}
	return lookupPin(cfg, pinKey)
}

// pinKeyForAddr extracts the pin key from the address a caller was asked to
// reach, BEFORE any resolution: the host as the user named it. That is the same
// key enforceDevicePin records under, so the two agree on what "this device"
// means. A resolved IP is deliberately never used as a key — it changes on
// ordinary DHCP churn — but an address the user typed as a literal IP is the
// name they asked for, so it keys a pin like any other host.
func pinKeyForAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.TrimSpace(addr)
	}
	return host
}

// expectedIdentityFor returns the asset identity pinned for pinKey, or nil when
// the host is unpinned or its pin predates asset ids. Nil means "first contact
// is permissive" — the posture that keeps legacy and unprovisioned devices
// working; the pin is written on the first successful connect.
//
// The lookup goes through lookupPin, so a pin recorded under any of the
// device's names — hostname, mesh name, or display name — is honoured here.
func expectedIdentityFor(pinKey string) *certs.WendyIdentity {
	pin, _, ok := governingPin(pinKey)
	if !ok || pin.AssetID == "" {
		return nil
	}
	return &certs.WendyIdentity{OrgID: int32(pin.OrgID), EntityType: "asset", EntityID: pin.AssetID}
}

// isPinned reports whether pinKey has any recorded pin under any of its
// candidate keys. A pinned host has been reached over mTLS before, so the
// ladder must not offer it the plaintext rung.
//
// This deliberately asks local state a question with a yes/no answer instead of
// inspecting the dial errors: the previous refusal was an accident of
// isCertRejectionError happening to match gRPC's "authentication handshake
// failed" wrapper, which any change to gRPC's error text would silently undo.
func isPinned(pinKey string) bool {
	_, _, ok := governingPin(pinKey)
	return ok
}

// pinCandidateKeys returns the keys a pin for pinKey may have been recorded
// under, most-specific first: the name the caller dialed, then — from the
// discovery-cache entry whose hostname matches it — that device's mesh name and
// display name. One device answers to all three, and different surfaces record
// under different ones: enforceDevicePin records the dialed mDNS hostname
// (wendyos-calm-zinnia), cloud seeding records the asset name the roster
// carries (calm-zinnia, which is the cache's display name), and mesh dials name
// the device by its mesh name. A cloud Asset carries only {Id, Name} — no
// hostname — so the cloud side cannot record under the dial key, and the
// reconciliation has to happen here on lookup.
//
// Duplicates are dropped under the same normalisation the pin store applies, so
// an alias that is only a cosmetic variant of the dialed name never produces a
// second, redundant candidate, and empty names are never looked up.
//
// Reading discovery-derived names to pick a key is safe FOR LOOKUP in a way
// that reading them to make a trust decision would not be: consulting an extra
// candidate can only ever FIND a pin, never discard one, so an attacker-chosen
// alias can at most impose a constraint that the real device fails — a stricter
// outcome, not a bypass. The trust decision itself stays on the certificate.
//
// That justification is about lookup and does not carry to clearing. A caller
// that DELETES every candidate turns the same attacker-chosen alias into a way
// to drop another device's pin, which is a bypass — see clearPinsGoverning,
// which consumes this list but removes an alias's pin only when it names the
// same device as the governing one.
func pinCandidateKeys(pinKey string) []string {
	if pinKey == "" {
		return nil
	}
	candidates := []string{pinKey}
	// Best effort by construction: cachedDeviceHostEntry reports false for an
	// unopenable cache, an unreadable one, and a plain miss alike, and every
	// one of those degrades to exactly the single-key list above.
	entry, ok := cachedDeviceHostEntry(pinKey)
	if !ok {
		return candidates
	}
	seen := map[string]bool{normalizeMDNSHost(pinKey): true}
	for _, alias := range []string{entry.MeshName, entry.DisplayName} {
		norm := normalizeMDNSHost(alias)
		if norm == "" || seen[norm] {
			continue
		}
		seen[norm] = true
		candidates = append(candidates, alias)
	}
	return candidates
}

// lookupPin resolves the pin governing pinKey across every candidate key,
// returning it, the key it was found under, and whether there was one.
//
// A cloud-sourced pin outranks a LAN-sourced one wherever each sits in the
// candidate order: cloud learned the binding from the org's cloud over an
// authenticated session, while a LAN pin records only what some host on the
// local network presented. Among pins of equal source the earliest candidate
// wins, which keeps the dialed name authoritative over an alias.
//
// That precedence yields to one invariant: an asset-less cloud pin never
// displaces an incumbent carrying an asset id. Cloud authority decides WHICH
// binding to believe, and applying it is worth nothing if it leaves
// expectedIdentityFor with no binding to enforce — a host constrained to one
// asset would become constrained to none, which is exactly the same-CA-host
// redirect this path exists to stop. An asset-less pin is a real state, not a
// hypothetical: config's EvaluateDevicePin carries a dedicated branch for a
// cloud-sourced pin with no asset id.
//
// Because the search only ever adds keys, a host pinned under pinKey stays
// pinned no matter what the cache says or fails to say — the property that lets
// the cache be consulted best-effort without a cache outage quietly switching
// enforcement off.
func lookupPin(cfg *config.Config, pinKey string) (config.DevicePin, string, bool) {
	if cfg == nil {
		return config.DevicePin{}, "", false
	}
	var best config.DevicePin
	var bestKey string
	found := false
	for _, key := range pinCandidateKeys(pinKey) {
		pin, ok := cfg.DevicePinFor(key)
		if !ok {
			continue
		}
		if !found {
			best, bestKey, found = pin, key, true
			continue
		}
		if best.Source != config.PinSourceCloud && pin.Source == config.PinSourceCloud &&
			(pin.AssetID != "" || best.AssetID == "") {
			best, bestKey = pin, key
		}
	}
	return best, bestKey, found
}

// errDeviceIdentityRefused is what every refusal in this package answers
// errors.Is to. It exists so a caller can tell "the device you asked for is not
// what answered" apart from "nothing answered" WITHOUT matching on message
// text: the picker's Bluetooth fallback turns on exactly that distinction, and
// a fallback that silently reaches the rejected device over a second transport
// is the refusal undone.
var errDeviceIdentityRefused = errors.New("device identity refused")

// deviceIdentityRefusalError carries a refusal's full user-facing text while
// staying recognisable to errors.Is. The text is the whole message rather than
// a wrap so the refusals read exactly as they did before this type existed.
type deviceIdentityRefusalError struct{ msg string }

func (e *deviceIdentityRefusalError) Error() string { return e.msg }

func (e *deviceIdentityRefusalError) Is(target error) bool {
	return target == errDeviceIdentityRefused
}

// refuseIdentity builds a refusal that errors.Is(err, errDeviceIdentityRefused)
// recognises. Every refusal raised because the wrong device answered — here and
// in device_pin.go — must go through it.
func refuseIdentity(format string, args ...any) error {
	return &deviceIdentityRefusalError{msg: fmt.Sprintf(format, args...)}
}

// identityRefusal renders a wrong-device rejection. Same text in interactive,
// JSON, and non-interactive modes — there is deliberately no "trust this?"
// prompt, because a MITM warning that can be dismissed gets dismissed.
func identityRefusal(pinKey string, im *certs.IdentityMismatchError) error {
	got := "no wendy identity"
	if im.GotAsset != "" {
		got = fmt.Sprintf("asset %s in organization %d", im.GotAsset, im.GotOrg)
	}
	return refuseIdentity(
		"device %q is pinned to asset %s in organization %d, but the host answering presented %s; refusing to connect — if this device was legitimately replaced or re-enrolled, run 'wendy device unpin %s'",
		pinKey, im.WantAsset, im.WantOrg, got, pinKey)
}

func pinnedHostWentUnauthenticatedError(pinKey string) error {
	return refuseIdentity(
		"device %q is pinned to an enrolled identity but no authenticated endpoint answered; refusing to fall back to an unauthenticated connection — if it was reflashed or factory reset, run 'wendy device unpin %s'",
		pinKey, pinKey)
}

// spkiRefusal renders the OTHER pin's rejection: the SPKI store's, which is
// keyed by the certificate's own asset URN rather than by hostname and fires
// when a device's public key changes while its pinned certificate is still
// valid.
//
// Without this the store's PinMismatchError reached the user only as whatever
// text survived gRPC's handshake wrapper — a message that names neither the
// host as the user knows it nor any way out, leaving hand-editing
// known_devices.json as the only recovery. The SPKI store has no hostname in
// it, so the key comes from the dial: pinKey when there is one, the store's own
// display name otherwise.
//
// The command it prints names pm.Key — the SPKI store's own key — not the
// hostname, even though the hostname is what the user recognises. Naming the
// hostname is what made this refusal a dead end for the two populations that
// hit it most: the picker and `wendy device list` dial the IP first, so the
// message named an IP that keys nothing and whose asset nothing in local state
// can derive; and agents that never advertise `orgid` (the Swift macOS agent,
// Linux agents before 2026-07-18) leave the discovery cache with no identity to
// derive it from either. pm.Key is in hand at the moment of refusal and is
// exactly what the store is keyed by, so `wendy device unpin <urn>` reaches the
// entry directly — and clears any config pin naming the same identity with it.
func spkiRefusal(pinKey string, pm *devicepin.PinMismatchError) error {
	named := pinKey
	if named == "" {
		named = pm.DisplayName
	}
	// A store key is always present in a real mismatch; fall back to the name
	// only so a hand-built error still points at something.
	unpinArg := pm.Key
	if unpinArg == "" {
		unpinArg = named
	}
	return refuseIdentity(
		"device %q presented a different certificate key than the one pinned for %s (pinned %s, now %s); refusing to connect — if its certificate was legitimately reissued, run 'wendy device unpin %s'",
		named, pm.Key, pm.Want, pm.Got, unpinArg)
}
