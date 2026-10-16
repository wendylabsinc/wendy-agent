package nanprovider

import (
	"encoding/hex"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func TestPresenceValidation(t *testing.T) {
	encode := func(s string) string { return hex.EncodeToString([]byte(s)) }
	tenant, err := meshid.ParseTenant("5b8c6c8e-2f9f-4b4a-9a4f-6e2d8c9a0b12")
	if err != nil {
		t.Fatal(err)
	}
	const id = "018b8657-2f1a-4d3e-9c1a-2b4f6a8c0d1e"
	peer, err := decodePresence(tenant, encode("wendy-nan:1:"+id))
	if err != nil || peer.UUID().String() != id || peer.Tenant() != tenant {
		t.Fatalf("presence: %v %v", peer, err)
	}
	for _, bad := range []string{
		"wendy-nan:1:64:460",
		"wendy-nan:3:" + id,
		"wendy-nan:1:018b8657-2f1a-4d3e-9c1a-2b4f6a8c0d1eextra",
		"wendy-nan:1:018B8657-2F1A-4D3E-9C1A-2B4F6A8C0D1E",
		"wendy-nan:1:{018b8657-2f1a-4d3e-9c1a-2b4f6a8c0d1e}",
		"wendy-nan:1:018b86572f1a4d3e9c1a2b4f6a8c0d1e",
		"wendy-nan:1:",
		"",
	} {
		if _, err := decodePresence(tenant, encode(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := decodePresence(tenant, "not hex"); err == nil {
		t.Fatal("accepted nonhex")
	}
}
func TestNDPEventParsing(t *testing.T) {
	kind, values := parseEvent("IFNAME=nan0 <3>NAN-NDP-CONNECTED peer=aa:bb:cc:dd:ee:ff ndp_id=1 peer_ndi=00:11:22:33:44:55 ssi=\n")
	if kind != "NAN-NDP-CONNECTED" || values["ndp_id"] != "1" || values["peer_ndi"] != "00:11:22:33:44:55" {
		t.Fatalf("%s %+v", kind, values)
	}
}

func TestPresenceAllowsExplicitNilUUID(t *testing.T) {
	tenant, err := meshid.ParseTenant("00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatal(err)
	}
	text := "wendy-nan:1:00000000-0000-0000-0000-000000000000"
	device, err := decodePresence(tenant, hex.EncodeToString([]byte(text)))
	if err != nil || !device.IsValid() {
		t.Fatalf("explicit nil UUID presence: %v, %v", device, err)
	}
}
