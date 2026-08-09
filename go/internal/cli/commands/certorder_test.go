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
