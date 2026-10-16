package localmesh

import "testing"

func TestSnapshotHasCheaperDirectLink(t *testing.T) {
	a := testDevice(t, testPrincipal(testTenantUUID, testDeviceAID))
	b := testDevice(t, testPrincipal(testTenantUUID, testDeviceBID))
	s := NodeSnapshot{Links: []PeerLink{
		{Device: a, Cost: 4096},
		{Device: a, Cost: 256},
		{Device: b, Cost: 4096},
	}}
	if !s.HasCheaperLink(a, 4096) {
		t.Fatal("same device's TCP link should make BLE redundant")
	}
	if s.HasCheaperLink(b, 4096) {
		t.Fatal("an unrelated peer must not suppress BLE")
	}
	if s.HasCheaperLink(a, 256) {
		t.Fatal("an equally priced link is not cheaper")
	}
}
