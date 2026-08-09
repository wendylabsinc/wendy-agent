package commands

import (
	"os"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func certsForOrgs(orgs ...int) []config.CertificateInfo {
	certs := make([]config.CertificateInfo, 0, len(orgs))
	for _, o := range orgs {
		certs = append(certs, config.CertificateInfo{OrganizationID: o})
	}
	return certs
}

func orgsOf(certs []config.CertificateInfo) []int {
	orgs := make([]int, 0, len(certs))
	for _, c := range certs {
		orgs = append(orgs, c.OrganizationID)
	}
	return orgs
}

func equalOrgs(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOrderCertsByOrg(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []int
		org  int
		have bool
		want []int
	}{
		{
			// The case this exists for: device is on the second org, so the
			// default order wastes a full handshake on org 2 every command.
			name: "moves the remembered org to the front",
			in:   []int{2, 9}, org: 9, have: true,
			want: []int{9, 2},
		},
		{
			name: "keeps relative order of the remaining certs",
			in:   []int{2, 5, 9, 7}, org: 9, have: true,
			want: []int{9, 2, 5, 7},
		},
		{
			name: "keeps relative order among certs sharing the remembered org",
			in:   []int{2, 9, 9, 5}, org: 9, have: true,
			want: []int{9, 9, 2, 5},
		},
		{
			// A stale memo must cost nothing beyond today's behaviour.
			name: "unknown org leaves the order untouched",
			in:   []int{2, 9}, org: 42, have: true,
			want: []int{2, 9},
		},
		{
			name: "no memo leaves the order untouched",
			in:   []int{2, 9}, org: 0, have: false,
			want: []int{2, 9},
		},
		{
			name: "single cert is returned untouched",
			in:   []int{9}, org: 9, have: true,
			want: []int{9},
		},
		{
			name: "all certs sharing the org leaves the order untouched",
			in:   []int{9, 9}, org: 9, have: true,
			want: []int{9, 9},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := certsForOrgs(tc.in...)
			got := orderCertsByOrg(in, tc.org, tc.have)
			if !equalOrgs(orgsOf(got), tc.want) {
				t.Errorf("orderCertsByOrg(%v, %d, %v) = %v, want %v", tc.in, tc.org, tc.have, orgsOf(got), tc.want)
			}
			// Reordering must never drop or invent a candidate: the dial loop
			// still has to be able to reach every cert the user holds.
			if len(got) != len(tc.in) {
				t.Errorf("orderCertsByOrg returned %d certs, want %d", len(got), len(tc.in))
			}
			// The caller's slice is indexed elsewhere, so it must not be
			// reordered in place.
			if !equalOrgs(orgsOf(in), tc.in) {
				t.Errorf("orderCertsByOrg mutated its input: got %v, want %v", orgsOf(in), tc.in)
			}
		})
	}
}

// withTempCertOrderCache redirects the memo at its resolver so the test never
// touches the developer's real cache directory on any platform.
func withTempCertOrderCache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	prev := certOrderCacheDir
	certOrderCacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { certOrderCacheDir = prev })
}

func TestCertOrgMemoRoundTrip(t *testing.T) {
	withTempCertOrderCache(t)

	if _, ok := preferredCertOrg("example-roundtrip.local"); ok {
		t.Fatal("preferredCertOrg reported a hit against an empty cache")
	}

	rememberCertOrg("example-roundtrip.local", 9)
	got, ok := preferredCertOrg("example-roundtrip.local")
	if !ok || got != 9 {
		t.Fatalf("preferredCertOrg after remember = (%d, %v), want (9, true)", got, ok)
	}

	// A later successful connect on a different org must replace the entry,
	// not accumulate — otherwise a device that moves orgs stays slow forever.
	rememberCertOrg("example-roundtrip.local", 2)
	got, ok = preferredCertOrg("example-roundtrip.local")
	if !ok || got != 2 {
		t.Fatalf("preferredCertOrg after re-remember = (%d, %v), want (2, true)", got, ok)
	}
}

// TestCertOrgMemoSurvivesCorruptFile pins the fallback: a corrupt memo must
// read as "no hint" so the dial loop behaves exactly as it did before, rather
// than erroring out a connection over a cache file.
func TestCertOrgMemoSurvivesCorruptFile(t *testing.T) {
	withTempCertOrderCache(t)

	path, err := certOrderPath()
	if err != nil {
		t.Fatalf("certOrderPath: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("writing corrupt memo: %v", err)
	}
	if _, ok := preferredCertOrg("example-corrupt.local"); ok {
		t.Error("preferredCertOrg reported a hit from a corrupt memo")
	}
	// A corrupt file must not wedge future writes either.
	rememberCertOrg("example-corrupt.local", 9)
	if got, ok := preferredCertOrg("example-corrupt.local"); !ok || got != 9 {
		t.Errorf("preferredCertOrg after recovery = (%d, %v), want (9, true)", got, ok)
	}
}

func TestPreferredCertOrgEmptyHost(t *testing.T) {
	withTempCertOrderCache(t)
	if _, ok := preferredCertOrg(""); ok {
		t.Error("preferredCertOrg(\"\") reported a hit; an empty host must never match")
	}
	// Must not panic or write a bogus entry.
	rememberCertOrg("", 9)
}

// withDevicePins redirects the device-pin lookup at its config seam so the test
// never reads the developer's real ~/.wendy/config.json.
func withDevicePins(t *testing.T, pins map[string]config.DevicePin) {
	t.Helper()
	prev := certOrderConfigLoad
	certOrderConfigLoad = func() (*config.Config, error) {
		return &config.Config{DevicePins: pins}, nil
	}
	t.Cleanup(func() { certOrderConfigLoad = prev })
}

// TestPreferredCertOrgForHostFallsBackToDevicePin is the case this exists for:
// an agent too old to advertise an mDNS orgid, on a machine whose cache
// directory was wiped, still has a durable org record in the config directory.
func TestPreferredCertOrgForHostFallsBackToDevicePin(t *testing.T) {
	withTempCertOrderCache(t) // empty memo
	withDevicePins(t, map[string]config.DevicePin{"orin": {OrgID: 9, CloudGRPC: "cloud.wendy.dev:443"}})

	got, ok := preferredCertOrgForHost("orin.local")
	if !ok || got != 9 {
		t.Fatalf("preferredCertOrgForHost = (%d, %v), want (9, true) — the pin is stored normalised, so a %q dial must hit it", got, ok, "orin.local")
	}
}

// TestPreferredCertOrgForHostMemoWinsOverPin pins the precedence: the memo
// records the org that last actually completed a probe against this exact host,
// so it must outrank the older, coarser device pin.
func TestPreferredCertOrgForHostMemoWinsOverPin(t *testing.T) {
	withTempCertOrderCache(t)
	withDevicePins(t, map[string]config.DevicePin{"orin": {OrgID: 9}})
	rememberCertOrg("orin.local", 2)

	got, ok := preferredCertOrgForHost("orin.local")
	if !ok || got != 2 {
		t.Fatalf("preferredCertOrgForHost = (%d, %v), want (2, true) — the memo must outrank the device pin", got, ok)
	}
}

func TestPreferredCertOrgForHostNoHint(t *testing.T) {
	withTempCertOrderCache(t)
	withDevicePins(t, nil)

	if org, ok := preferredCertOrgForHost("unknown.local"); ok {
		t.Errorf("preferredCertOrgForHost = (%d, true), want no hit when neither source knows the host", org)
	}
	// A pin recorded with no org carries no hint and must not read as org 0.
	withDevicePins(t, map[string]config.DevicePin{"orin": {CloudGRPC: "cloud.wendy.dev:443"}})
	if org, ok := preferredCertOrgForHost("orin.local"); ok {
		t.Errorf("preferredCertOrgForHost = (%d, true), want no hit for a pin with no org", org)
	}
}

func TestPreferredCertOrgForHostSurvivesUnreadableConfig(t *testing.T) {
	withTempCertOrderCache(t)
	prev := certOrderConfigLoad
	certOrderConfigLoad = func() (*config.Config, error) { return nil, os.ErrPermission }
	t.Cleanup(func() { certOrderConfigLoad = prev })

	// Must read as "no hint" rather than panic on the nil config: an ordering
	// hint is never worth failing a connection over.
	if org, ok := preferredCertOrgForHost("orin.local"); ok {
		t.Errorf("preferredCertOrgForHost = (%d, true), want no hit when the config cannot be read", org)
	}
}

func TestPromoteOrgNext(t *testing.T) {
	for _, tc := range []struct {
		name  string
		certs []int
		order []int
		pos   int
		org   int32
		want  []int
		moved bool
	}{
		{
			// The case this exists for: an agent advertising no orgid TXT record.
			// The first probe fails but the device's server cert names org 9, so
			// org 9 is tried next instead of last.
			name:  "promotes the observed org to the next position",
			certs: []int{2, 5, 7, 9}, order: []int{0, 1, 2, 3}, pos: 0, org: 9,
			want: []int{0, 3, 1, 2}, moved: true,
		},
		{
			name:  "defers skipped certs without reordering them among themselves",
			certs: []int{2, 5, 7, 3, 9}, order: []int{0, 1, 2, 3, 4}, pos: 0, org: 9,
			want: []int{0, 4, 1, 2, 3}, moved: true,
		},
		{
			// Same-org failure (clock skew, expired cert): the only cert for the
			// observed org is the one that just failed, so there is nothing left
			// to promote and the caller's diagnostics must stand.
			name:  "no-op when the observed org was already probed",
			certs: []int{9, 2, 5}, order: []int{0, 1, 2}, pos: 0, org: 9,
			want: []int{0, 1, 2}, moved: false,
		},
		{
			// A genuine cross-org device we hold no cert for.
			name:  "no-op when we hold no cert for the observed org",
			certs: []int{2, 5}, order: []int{0, 1}, pos: 0, org: 42,
			want: []int{0, 1}, moved: false,
		},
		{
			name:  "no-op at the last position",
			certs: []int{2, 9}, order: []int{0, 1}, pos: 1, org: 9,
			want: []int{0, 1}, moved: false,
		},
		{
			name:  "already next is reported as acted on and changes nothing",
			certs: []int{2, 9, 5}, order: []int{0, 1, 2}, pos: 0, org: 9,
			want: []int{0, 1, 2}, moved: true,
		},
		{
			name:  "promotes relative to a mid-scan position",
			certs: []int{2, 5, 7, 9}, order: []int{0, 1, 2, 3}, pos: 1, org: 9,
			want: []int{0, 1, 3, 2}, moved: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certs := certsForOrgs(tc.certs...)
			order := append([]int(nil), tc.order...)
			moved := promoteOrgNext(order, tc.pos, certs, tc.org)
			if moved != tc.moved {
				t.Errorf("promoteOrgNext(...) = %v, want %v", moved, tc.moved)
			}
			if !equalOrgs(order, tc.want) {
				t.Errorf("order = %v, want %v", order, tc.want)
			}
			// The reorder must stay a permutation: the ladder still has to reach
			// every cert the user holds, or a jump could strand the right one.
			seen := make(map[int]bool, len(order))
			for _, idx := range order {
				if seen[idx] {
					t.Fatalf("order %v repeats index %d; a cert was dropped", order, idx)
				}
				seen[idx] = true
			}
			if len(seen) != len(tc.order) {
				t.Errorf("order holds %d distinct indices, want %d", len(seen), len(tc.order))
			}
			// Entries at or before pos are already probed; moving them would
			// re-probe a cert and could loop.
			for i := 0; i <= tc.pos && i < len(order); i++ {
				if order[i] != tc.order[i] {
					t.Errorf("order[%d] = %d, want %d: already-probed entries must not move", i, order[i], tc.order[i])
				}
			}
		})
	}
}
