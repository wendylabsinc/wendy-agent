package localmesh

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/WendyOS/babel"
)

func TestStateStoreRestartAndIdentityBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	device := testDevice(t, testPrincipal(testTenantUUID, testDeviceAID))
	id, err := instanceRouterID()
	if err != nil {
		t.Fatal(err)
	}
	s, state, err := OpenStateStore(path, device, id)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := babel.New(babel.Config{RouterID: id})
	cp := engine.Checkpoint()
	state.Revision = 123
	state.Babel = &cp
	if err = s.Save(state); err != nil {
		t.Fatal(err)
	}
	_, restored, err := OpenStateStore(path, device, id)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 123 || restored.Babel.RouterID != id {
		t.Fatal(restored)
	}
	// The same device UUID under another tenant is a different instance.
	if _, _, err = OpenStateStore(path, testDevice(t, testPrincipal(testOtherTenant, testDeviceAID)), id); err == nil {
		t.Fatal("cross-tenant safety state reused")
	}
}

// TestStateStoreRefusesForeignRouterID: after an identity change the instance
// must not quietly resume on the checkpoint written by its old routing identity.
func TestStateStoreRefusesForeignRouterID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	device := testDevice(t, testPrincipal(testTenantUUID, testDeviceAID))
	id, err := instanceRouterID()
	if err != nil {
		t.Fatal(err)
	}
	s, state, err := OpenStateStore(path, device, id)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := babel.New(babel.Config{RouterID: id})
	cp := engine.Checkpoint()
	state.Babel = &cp
	if err = s.Save(state); err != nil {
		t.Fatal(err)
	}
	other, err := babel.RouterIDFromMAC(net.HardwareAddr{0x02, 0, 0, 0, 0, 0x63})
	if err != nil {
		t.Fatal(err)
	}
	if _, restored, err := OpenStateStore(path, device, other); err == nil {
		t.Fatalf("checkpoint adopted under a foreign router ID: %+v", restored)
	}
	// A checkpoint whose own router ID disagrees with the file's pin is refused
	// even when the pin matches this instance.
	if _, _, err = OpenStateStore(path, device, id); err != nil {
		t.Fatal(err)
	}
	tampered := state
	tampered.Babel = &babel.Checkpoint{RouterID: other, Seqno: cp.Seqno}
	if err = s.Save(tampered); err != nil {
		t.Fatal(err)
	}
	if _, _, err = OpenStateStore(path, device, id); err == nil {
		t.Fatal("checkpoint from another router was resumed")
	}
}
