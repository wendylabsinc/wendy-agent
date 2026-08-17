package rcm

import (
	"strings"
	"testing"
)

const testCanonicalECID = "80012641783de2442400000016ff80c0"

// The T234 family enumerates one recovery PID per module SKU (0x7<module>23).
// Detection must accept all of them — matching only the AGX Orin PID made an
// Orin Nano in recovery mode invisible to the CLI (WDY-1888).
func TestT234FamilyRecoveryPIDs(t *testing.T) {
	tests := []struct {
		pid       uint16
		agx, nano bool
	}{
		{ProductOrinAGX32, true, false},
		{ProductOrinAGX64, true, false},
		{ProductOrinNX16, false, false},
		{ProductOrinNX8, false, false},
		{ProductOrinNano8, false, true},
		{ProductOrinNano4, false, true},
	}
	for _, tc := range tests {
		d := RecoveryDevice{Product: tc.pid}
		if !IsT234RecoveryPID(tc.pid) || !d.IsOrin() {
			t.Errorf("PID 0x%04x not recognized as T234", tc.pid)
		}
		if d.IsThor() {
			t.Errorf("PID 0x%04x misclassified as Thor", tc.pid)
		}
		if d.IsOrinAGX() != tc.agx || d.IsOrinNano() != tc.nano {
			t.Errorf("PID 0x%04x: IsOrinAGX=%v IsOrinNano=%v, want %v/%v", tc.pid, d.IsOrinAGX(), d.IsOrinNano(), tc.agx, tc.nano)
		}
		if got := d.Describe(); !strings.Contains(got, "T234 recovery") || strings.Contains(got, "Nano 8GB") || strings.Contains(got, "AGX Orin") {
			t.Errorf("Describe() for PID 0x%04x made a SKU claim: %q", tc.pid, got)
		}
	}
}

func TestNonT234PIDsRejected(t *testing.T) {
	thor := RecoveryDevice{Product: ProductThor}
	if thor.IsOrin() || thor.IsOrinAGX() || thor.IsOrinNano() || !thor.IsThor() {
		t.Errorf("Thor PID misclassified: %+v", thor)
	}
	if IsT234RecoveryPID(ProductThor) || IsT234RecoveryPID(0x7123) || IsT234RecoveryPID(0x0104) {
		t.Error("non-T234 PID accepted as T234 recovery PID")
	}
}

func TestRecoveryECIDDigestIsCanonicalAndDomainSeparated(t *testing.T) {
	want := RecoveryECIDDigest(testCanonicalECID)
	const literal = "sha256:52c5b9de12b1f1943a441df0ecdea9f3eb94c7f64b98968855390f14c6e6e05c"
	if want != literal {
		t.Fatalf("domain-separated digest = %q, want %q", want, literal)
	}
	if got := RecoveryECIDDigest(strings.ToUpper(testCanonicalECID)); got != want {
		t.Fatalf("uppercase ECID digest = %q, want %q", got, want)
	}
	if got := RecoveryECIDDigest("wendyos-recovery-ecid-v1\n" + testCanonicalECID); got != "" {
		t.Fatalf("non-ECID input unexpectedly hashed: %q", got)
	}
	if got := RecoveryECIDFromUSBSerial("0C08FF6100000042442ED38714621008"); got != testCanonicalECID {
		t.Fatalf("USB serial normalized to %q, want %q", got, testCanonicalECID)
	}
}

func TestRecoverySelectorValidation(t *testing.T) {
	digest := RecoveryECIDDigest(testCanonicalECID)
	selector, err := NewRecoverySelector("1-2.3", strings.ToUpper(digest[:7])+digest[7:])
	if err == nil {
		// The algorithm prefix is deliberately canonical/lowercase. This keeps
		// the external contract exact and avoids multiple serialized spellings.
		t.Fatalf("uppercase digest prefix accepted: %+v", selector)
	}
	selector, err = NewRecoverySelector("1-2.3", "sha256:"+strings.ToUpper(digest[len("sha256:"):]))
	if err != nil || selector.ExpectedECIDDigest != digest {
		t.Fatalf("uppercase hex normalization = %+v, %v", selector, err)
	}
	for _, path := range []string{" 1-2", "1-2\n", strings.Repeat("x", maxRecoveryPathBytes+1)} {
		if _, err := NewRecoverySelector(path, digest); err == nil {
			t.Errorf("unsafe path accepted (length %d)", len(path))
		}
	}
	if _, err := NewRecoverySelector("1-2", "sha256:not-a-digest"); err == nil {
		t.Fatal("invalid digest accepted")
	}
	if _, err := NewRecoverySelector("1-2", ""); err == nil {
		t.Fatal("path-only selector accepted")
	}
	if _, err := NewRecoverySelector("", digest); err == nil {
		t.Fatal("digest-only selector accepted")
	}
	if selector, err := NewRecoverySelector("", ""); err != nil || !selector.IsZero() {
		t.Fatalf("empty interactive selector = %+v, %v", selector, err)
	}
}

func TestSelectRecoveryDeviceFailsClosed(t *testing.T) {
	wanted := RecoveryDevice{PathKey: "1-2", Product: ProductOrinNano8, ECID: testCanonicalECID}
	other := RecoveryDevice{PathKey: "1-3", Product: ProductOrinNano8, ECID: "10012641783de2442400000016ff80c0"}
	selector, err := NewRecoverySelector(wanted.PathKey, wanted.ECIDDigest())
	if err != nil {
		t.Fatal(err)
	}
	got, err := SelectRecoveryDevice([]RecoveryDevice{other, wanted}, selector)
	if err != nil || got.PathKey != wanted.PathKey {
		t.Fatalf("selection = %+v, %v", got, err)
	}

	cases := []struct {
		name string
		devs []RecoveryDevice
		sel  RecoverySelector
	}{
		{"zero", nil, selector},
		{"mismatch", []RecoveryDevice{other}, selector},
		{"missing ECID", []RecoveryDevice{{PathKey: wanted.PathKey, Product: ProductOrinNano8}}, selector},
		{"multiple", []RecoveryDevice{wanted, wanted}, selector},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SelectRecoveryDevice(tc.devs, tc.sel)
			if err == nil {
				t.Fatal("expected fail-closed selection error")
			}
			if strings.Contains(strings.ToLower(err.Error()), testCanonicalECID) || strings.Contains(err.Error(), wanted.ECIDDigest()) {
				t.Fatalf("identity leaked in error: %v", err)
			}
		})
	}

	tooMany := make([]RecoveryDevice, MaxRecoveryDevices+1)
	if _, err := SelectRecoveryDevice(tooMany, selector); err == nil {
		t.Fatal("oversized discovery result accepted")
	}
}

func TestDescribeNeverExposesRecoveryIdentity(t *testing.T) {
	d := RecoveryDevice{PathKey: "1-2", Product: ProductThor, ECID: testCanonicalECID}
	got := d.Describe()
	if strings.Contains(strings.ToLower(got), testCanonicalECID) || strings.Contains(got, d.ECIDDigest()) {
		t.Fatalf("Describe leaked hardware identity: %q", got)
	}
	if !strings.Contains(got, "ECID available") {
		t.Fatalf("Describe lost availability signal: %q", got)
	}
}

func TestPinnedSelectorNeverDegrades(t *testing.T) {
	valid := RecoveryDevice{PathKey: "1-2", Product: ProductThor, ECID: testCanonicalECID}
	selector, err := valid.PinnedSelector()
	if err != nil || selector.PathKey == "" || selector.ExpectedECIDDigest == "" {
		t.Fatalf("valid pin = %+v, %v", selector, err)
	}
	for _, dev := range []RecoveryDevice{
		{Product: ProductThor, ECID: testCanonicalECID},
		{PathKey: "1-2", Product: ProductThor},
	} {
		if _, err := dev.PinnedSelector(); err == nil {
			t.Fatalf("partial device pin accepted: %+v", dev)
		}
	}
}
