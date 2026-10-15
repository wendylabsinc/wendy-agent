package localmesh

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTCPConfigRequiresReciprocalConcretePeers: a configured peer is named by
// device UUID, in this device's own tenant, once, with a concrete address.
func TestTCPConfigRequiresReciprocalConcretePeers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-mesh.json")
	self := testDevice(t, testPrincipal(testTenantUUID, testDeviceAID))
	peer := testDevice(t, testPrincipal(testTenantUUID, testDeviceBID))
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"listen":"127.0.0.1:7001","peers":[{"device":"` + peer.UUID().String() + `","address":"127.0.0.1:7002"}]}`, true},
		{`{"listen":"127.0.0.1:7001","peers":[{"device":"` + self.UUID().String() + `","address":"127.0.0.1:7002"}]}`, false},
		{`{"listen":"127.0.0.1:7001","peers":[{"device":"` + peer.UUID().String() + `","address":"0.0.0.0:7002"}]}`, false},
		{`{"listen":"127.0.0.1:7001","peers":[{"device":"` + peer.UUID().String() + `","address":"127.0.0.1:7002"},{"device":"` + peer.UUID().String() + `","address":"127.0.0.1:7003"}]}`, false},
		{`{"listen":"127.0.0.1:7001","peers":[{"device":"2b1c0000-0000-4000-8000-000000000460","address":"127.0.0.1:7002"}]}`, true},
		{`{"listen":"127.0.0.1:7001","peers":[{"device":"orin","address":"127.0.0.1:7002"}]}`, false},
		{`{"listen":"127.0.0.1:7001","peers":[{"asset":2,"address":"127.0.0.1:7002"}]}`, false},
		{`{"listen":"127.0.0.1:7001","peers":[],"unknown":true}`, false},
		{`{"listen":"127.0.0.1:7001","peers":[]} {}`, false},
	} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadTCPConfig(path, self)
		if (err == nil && cfg != nil) != tc.valid {
			t.Errorf("body %s: valid=%v, err=%v", tc.body, tc.valid, err)
		}
	}
	// The parsed peers are devices, in file order, ready to key a link.
	if err := os.WriteFile(path, []byte(`{"listen":"127.0.0.1:7001","peers":[{"device":"`+peer.UUID().String()+`","address":"127.0.0.1:7002"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadTCPConfig(path, self)
	if err != nil {
		t.Fatal(err)
	}
	devices := cfg.Devices()
	if len(devices) != 1 || devices[0].Principal() != peer.Principal() {
		t.Fatalf("configured peers are not the configured devices: %+v", devices)
	}
}
