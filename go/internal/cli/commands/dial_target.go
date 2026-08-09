package commands

import (
	"fmt"
	"net"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
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
	// Addr is the host:port actually dialed.
	Addr string
	// Expected constrains the peer certificate. Non-nil only when a pin (or a
	// cloud-seeded value) names a specific asset.
	Expected *certs.WendyIdentity
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

// newDialTarget resolves the pin for pinKey and returns a target constrained by
// it. Key resolution deliberately may read discovery-derived names: choosing
// the wrong key can only ever produce a mismatch — a stricter outcome — never
// a bypass, because the trust decision itself stays on the certificate.
func newDialTarget(pinKey, addr string) dialTarget {
	return dialTarget{PinKey: pinKey, Addr: addr, Expected: expectedIdentityFor(pinKey)}
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
func expectedIdentityFor(pinKey string) *certs.WendyIdentity {
	if pinKey == "" {
		return nil
	}
	cfg, err := loadConfigForPinFn()
	if err != nil {
		return nil
	}
	pin, ok := cfg.DevicePinFor(pinKey)
	if !ok || pin.AssetID == "" {
		return nil
	}
	return &certs.WendyIdentity{OrgID: int32(pin.OrgID), EntityType: "asset", EntityID: pin.AssetID}
}

// isPinned reports whether pinKey has any recorded pin. A pinned host has been
// reached over mTLS before, so the ladder must not offer it the plaintext rung.
//
// This deliberately asks local state a question with a yes/no answer instead of
// inspecting the dial errors: the previous refusal was an accident of
// isCertRejectionError happening to match gRPC's "authentication handshake
// failed" wrapper, which any change to gRPC's error text would silently undo.
func isPinned(pinKey string) bool {
	if pinKey == "" {
		return false
	}
	cfg, err := loadConfigForPinFn()
	if err != nil {
		return false
	}
	_, ok := cfg.DevicePinFor(pinKey)
	return ok
}

// identityRefusal renders a wrong-device rejection. Same text in interactive,
// JSON, and non-interactive modes — there is deliberately no "trust this?"
// prompt, because a MITM warning that can be dismissed gets dismissed.
func identityRefusal(pinKey string, im *certs.IdentityMismatchError) error {
	got := "no wendy identity"
	if im.GotAsset != "" {
		got = fmt.Sprintf("asset %s in organization %d", im.GotAsset, im.GotOrg)
	}
	return fmt.Errorf(
		"device %q is pinned to asset %s in organization %d, but the host answering presented %s; refusing to connect — if this device was legitimately replaced or re-enrolled, run 'wendy device unpin %s'",
		pinKey, im.WantAsset, im.WantOrg, got, pinKey)
}

func pinnedHostWentUnauthenticatedError(pinKey string) error {
	return fmt.Errorf(
		"device %q is pinned to an enrolled identity but no authenticated endpoint answered; refusing to fall back to an unauthenticated connection — if it was reflashed or factory reset, run 'wendy device unpin %s'",
		pinKey, pinKey)
}
