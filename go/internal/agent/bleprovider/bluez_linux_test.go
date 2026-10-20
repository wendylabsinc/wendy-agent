//go:build linux

package bleprovider

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func TestRuntimeRetryHistoryBoundsAndExpires(t *testing.T) {
	r := &runtime{cfg: Config{TargetPeers: 3}, active: map[meshid.Device]struct{}{}, nextAttempt: map[meshid.Device]time.Time{}}
	for i := range 4096 {
		device := fixtureDevice(fmt.Sprintf("00000000-0000-4000-8000-%012x", i))
		if !r.claim(device) {
			t.Fatal("released peer consumed active capacity")
		}
		r.release(device)
	}
	if len(r.nextAttempt) > 128 {
		t.Fatalf("retry history retained %d distinct peers", len(r.nextAttempt))
	}
	for device := range r.nextAttempt {
		r.nextAttempt[device] = time.Now().Add(-time.Second)
	}
	peer := candidate{hint: deviceHint(lowDevice), address: "02:00:00:00:00:01", addressType: "public", psm: DefaultPSM}
	if !r.claimCandidate(peer) || len(r.nextAttempt) != 0 {
		t.Fatalf("expired cooldowns block a new peer or remain retained: %d", len(r.nextAttempt))
	}
}

func TestCandidateAdmissionKeepsEqualHintsDistinctAndBoundsRetries(t *testing.T) {
	r := &runtime{cfg: Config{TargetPeers: 2}, active: map[meshid.Device]struct{}{}, nextAttempt: map[meshid.Device]time.Time{}}
	peer := func(i int) candidate {
		return candidate{hint: deviceHint(lowDevice), addressType: "public", address: fmt.Sprintf("02:00:00:%02X:%02X:%02X", i>>16&255, i>>8&255, i&255)}
	}
	first, second := peer(1), peer(2)
	if !r.claimCandidate(first) || !r.claimCandidate(second) || r.claimCandidate(peer(3)) {
		t.Fatal("equal hints were merged or pending setup exceeded capacity")
	}
	r.releaseCandidate(first)
	r.releaseCandidate(second)
	if r.claimCandidate(first) {
		t.Fatal("released radio bypassed its retry cooldown")
	}
	for i := 3; i < 4096; i++ {
		p := peer(i)
		if r.claimCandidate(p) {
			r.releaseCandidate(p)
		}
	}
	if len(r.pending) != 0 || len(r.radioRetry) > maxPeerRetryHistory {
		t.Fatalf("pending=%d retry history=%d", len(r.pending), len(r.radioRetry))
	}
	for key := range r.radioRetry {
		r.radioRetry[key] = time.Now().Add(-time.Second)
	}
	if !r.claimCandidate(peer(4096)) || len(r.radioRetry) != 0 {
		t.Fatal("expired radio attempts prevented a new candidate")
	}
	r.releaseCandidate(peer(4096))
	r.known[first.key()] = lowFixture
	r.active[lowFixture] = struct{}{}
	if r.claimCandidate(first) {
		t.Fatal("known active principal was dialed again")
	}
	delete(r.active, lowFixture)
	first.hint[0]++
	if !r.claimCandidate(first) {
		t.Fatal("changed discovery hint could not start fresh authentication")
	}
	if _, exists := r.known[first.key()]; exists {
		t.Fatal("changed radio hint retained a previous principal pin")
	}
}

func TestCandidatesRequireMatchingServiceDataAndLowerDevice(t *testing.T) {
	uuid := ServiceUUID(testTenant)
	otherUUID := ServiceUUID(otherTenant)
	a, _ := NewAdvertisement(lowDevice, "field", 0x81)
	raw, _ := a.MarshalBinary()
	wrong, _ := NewAdvertisement(peerDevice, "other", 0x81)
	wrongRaw, _ := wrong.MarshalBinary()
	props := func(address, kind, serviceUUID string, data []byte) map[string]dbus.Variant {
		return map[string]dbus.Variant{
			"Address": dbus.MakeVariant(address), "AddressType": dbus.MakeVariant(kind),
			"ServiceData": dbus.MakeVariant(map[string]dbus.Variant{serviceUUID: dbus.MakeVariant(data)}),
		}
	}
	objects := managedObjects{
		"/org/bluez/hci0/dev_A": {deviceInterface: props("AA:BB:CC:DD:EE:01", "public", uuid, raw)},
		"/org/bluez/hci0/dev_B": {deviceInterface: props("AA:BB:CC:DD:EE:02", "random", otherUUID, raw)},
		"/org/bluez/hci0/dev_C": {deviceInterface: props("AA:BB:CC:DD:EE:03", "public", uuid, wrongRaw)},
		"/org/bluez/hci1/dev_D": {deviceInterface: props("AA:BB:CC:DD:EE:04", "public", uuid, raw)},
	}
	got := candidates(objects, "/org/bluez/hci0", uuid, "field", selfFixture)
	if len(got) != 1 || got[0].hint != deviceHint(lowDevice) || got[0].address != "AA:BB:CC:DD:EE:01" || got[0].path != "/org/bluez/hci0/dev_A" {
		t.Fatalf("candidates = %+v", got)
	}
	if got = candidates(objects, "/org/bluez/hci0", uuid, "field", lowFixture); len(got) != 1 {
		t.Fatalf("equal hint must reach authentication: %+v", got)
	}
}

func TestBlueZOwnerChangeRequiresProviderRestart(t *testing.T) {
	if err := verifyBlueZOwner(":1.42", ":1.42"); err != nil {
		t.Fatalf("stable BlueZ owner: %v", err)
	}
	for _, current := range []string{":1.43", ""} {
		if err := verifyBlueZOwner(":1.42", current); !errors.Is(err, errBlueZOwnerChanged) {
			t.Fatalf("owner %q: got %v, want restart", current, err)
		}
	}
	r := &runtime{cfg: Config{Logger: zap.NewNop()}, owner: ":1.42", ownerLookup: func(context.Context, *dbus.Conn) (string, error) {
		return ":1.43", nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.scanLoop(ctx, nil, "/org/bluez/hci0"); !errors.Is(err, errBlueZOwnerChanged) {
		t.Fatalf("scan loop after BlueZ restart: got %v, want immediate provider restart", err)
	}
}

func TestRuntimeClaimsTargetAndSuppressesDuplicate(t *testing.T) {
	r := &runtime{cfg: Config{TargetPeers: 3}, active: map[meshid.Device]struct{}{}, nextAttempt: map[meshid.Device]time.Time{}}
	peers := []meshid.Device{fixtureDevice("00000000-0000-4000-8000-000000000001"), fixtureDevice("00000000-0000-4000-8000-000000000002"), fixtureDevice("00000000-0000-4000-8000-000000000003")}
	fourth := fixtureDevice("00000000-0000-4000-8000-000000000004")
	for _, device := range peers {
		if !r.claim(device) {
			t.Fatalf("could not claim %s", device.UUID())
		}
	}
	if r.claim(peers[0]) || r.claim(fourth) {
		t.Fatal("duplicate or fourth peer accepted")
	}
	r.release(peers[0])
	if !r.claim(fourth) {
		t.Fatal("slot not reusable")
	}
}

func TestAdvertisementTxPowerCapabilities(t *testing.T) {
	manager := func(features []string, capability any) map[string]dbus.Variant {
		return map[string]dbus.Variant{
			"SupportedFeatures": dbus.MakeVariant(features),
			"SupportedCapabilities": dbus.MakeVariant(map[string]dbus.Variant{
				"MaxTxPower": dbus.MakeVariant(capability),
			}),
		}
	}
	tests := []struct {
		name    string
		props   map[string]dbus.Variant
		want    int16
		present bool
	}{
		{"manager lookup failed", nil, 0, false},
		{"jetson", manager([]string{"CanSetTxPower"}, int16(8)), 8, true},
		{"pi without feature", manager([]string{"Other"}, int16(8)), 0, false},
		{"malformed features", map[string]dbus.Variant{"SupportedFeatures": dbus.MakeVariant("CanSetTxPower")}, 0, false},
		{"missing features", map[string]dbus.Variant{"SupportedCapabilities": manager(nil, int16(8))["SupportedCapabilities"]}, 0, false},
		{"missing capabilities", map[string]dbus.Variant{"SupportedFeatures": dbus.MakeVariant([]string{"CanSetTxPower"})}, 0, false},
		{"wrong capability type", manager([]string{"CanSetTxPower"}, int32(8)), 0, false},
		{"zero is valid", manager([]string{"CanSetTxPower"}, int16(0)), 0, true},
		{"clamp bluez upper limit", manager([]string{"CanSetTxPower"}, int16(30)), 20, true},
		{"bluez lower limit", manager([]string{"CanSetTxPower"}, int16(-127)), -127, true},
		{"invalid controller maximum", manager([]string{"CanSetTxPower"}, int16(-128)), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, present := advertisementTxPower(tt.props)
			if got != tt.want || present != tt.present {
				t.Fatalf("power=(%d,%v), want (%d,%v)", got, present, tt.want, tt.present)
			}
			properties := advertisementProperties("uuid", []byte{1}, tt.props)
			power, exists := properties["TxPower"]
			if exists != tt.present {
				t.Fatalf("TxPower exists=%v, want %v", exists, tt.present)
			}
			if exists {
				value, ok := power.Value.(int16)
				if !ok || value != tt.want {
					t.Fatalf("TxPower=%T(%v), want int16(%d)", power.Value, power.Value, tt.want)
				}
			}
			if _, ok := properties["ServiceData"]; !ok {
				t.Fatal("advertising payload removed")
			}
		})
	}
}

func TestAdvertisementRegistrationFallsBackToDefaultPower(t *testing.T) {
	properties := advertisementProperties("uuid", []byte{1}, map[string]dbus.Variant{
		"SupportedFeatures": dbus.MakeVariant([]string{"CanSetTxPower"}),
		"SupportedCapabilities": dbus.MakeVariant(map[string]dbus.Variant{
			"MaxTxPower": dbus.MakeVariant(int16(8)),
		}),
	})
	var exported []bool
	registrations := 0
	err := registerAdvertisementWithFallback(properties, func(p map[string]*prop.Prop) error {
		_, requested := p["TxPower"]
		exported = append(exported, requested)
		return nil
	}, func() error {
		registrations++
		if registrations == 1 {
			return errors.New("BlueZ rejected TxPower")
		}
		return nil
	})
	if err != nil || registrations != 2 || len(exported) != 2 || !exported[0] || exported[1] {
		t.Fatalf("fallback err=%v registrations=%d exported=%v", err, registrations, exported)
	}
	if _, ok := properties["ServiceData"]; !ok {
		t.Fatal("fallback removed service advertisement")
	}

	// A failure without the optional property must not be retried.
	registrations = 0
	err = registerAdvertisementWithFallback(advertisementProperties("uuid", []byte{1}, nil), func(map[string]*prop.Prop) error {
		return nil
	}, func() error {
		registrations++
		return errors.New("adapter unavailable")
	})
	if err == nil || registrations != 1 {
		t.Fatalf("ordinary failure err=%v registrations=%d", err, registrations)
	}

	// A failure with interval props (e.g. a future re-introduction) must
	// also fall back to defaults: strip intervals and retry once.
	withIntervals := advertisementProperties("uuid", []byte{1}, nil)
	withIntervals["MinInterval"] = &prop.Prop{Value: uint16(160)}
	withIntervals["MaxInterval"] = &prop.Prop{Value: uint16(320)}
	registrations = 0
	err = registerAdvertisementWithFallback(withIntervals, func(map[string]*prop.Prop) error {
		return nil
	}, func() error {
		registrations++
		if registrations == 1 {
			return errors.New("BlueZ rejected intervals")
		}
		return nil
	})
	if err != nil || registrations != 2 {
		t.Fatalf("interval fallback err=%v registrations=%d", err, registrations)
	}
	if _, ok := withIntervals["MinInterval"]; ok {
		t.Fatal("fallback left MinInterval in place")
	}
	if _, ok := withIntervals["MaxInterval"]; ok {
		t.Fatal("fallback left MaxInterval in place")
	}
}

func TestAdvertisementRelaxedIntervalsPresent(t *testing.T) {
	// golden16: intervals reverted — BlueZ 5.87 rejects them outright.
	// Advertisement must carry only required props; fallback must strip
	// any optional interval/power props on registration failure.
	properties := advertisementProperties("uuid", []byte{1}, nil)
	if _, ok := properties["MinInterval"]; ok {
		t.Fatal("MinInterval present (reverted in golden16)")
	}
	if _, ok := properties["MaxInterval"]; ok {
		t.Fatal("MaxInterval present (reverted in golden16)")
	}
}

func TestCandidatesDoNotDeduplicateCollidingHints(t *testing.T) {
	adv, _ := NewAdvertisement(lowDevice, "field", DefaultPSM)
	raw, _ := adv.MarshalBinary()
	first := meshDeviceProps(ServiceUUID(testTenant), raw)
	second := meshDeviceProps(ServiceUUID(testTenant), raw)
	second["Address"] = dbus.MakeVariant("AA:BB:CC:DD:EE:02")
	objects := managedObjects{
		"/org/bluez/hci0/dev_A": {deviceInterface: first},
		"/org/bluez/hci0/dev_B": {deviceInterface: second},
	}
	if peers := candidates(objects, "/org/bluez/hci0", ServiceUUID(testTenant), "field", selfFixture); len(peers) != 2 {
		t.Fatalf("colliding candidates lost: %+v", peers)
	}
}
