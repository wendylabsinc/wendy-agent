//go:build linux

package localmesh

import (
	"net"
	"testing"

	"github.com/hashicorp/mdns"
	"github.com/miekg/dns"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func TestLANDiscoveryVersionCompatibility(t *testing.T) {
	self, peer := lanDevice(t, 445), lanDevice(t, 460)
	base := []string{"principal=" + peer.Principal(), "mesh=" + lanMeshHash(self.Tenant())}
	for _, tt := range []struct {
		name  string
		extra []string
		want  bool
	}{
		{"legacy", nil, true},
		{"v1", []string{"version=1"}, true},
		{"v1 with extension", []string{"version=1", "future=opaque"}, true},
		{"empty", []string{"version="}, false},
		{"future", []string{"version=2"}, false},
		{"zero", []string{"version=0"}, false},
		{"noncanonical", []string{"version=01"}, false},
		{"space", []string{"version=1 "}, false},
		{"duplicate", []string{"version=1", "version=1"}, false},
		{"conflicting", []string{"version=1", "version=2"}, false},
		{"malformed", []string{"version"}, false},
		{"missing mesh", []string{"version=1"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := append(append([]string(nil), base...), tt.extra...)
			if tt.name == "missing mesh" {
				info = []string{base[0], "version=1"}
			}
			entry := &mdns.ServiceEntry{Port: LANPort, AddrV4: net.IPv4(192, 168, 2, 3), InfoFields: info}
			got, _, ok := parseLANEntry(entry, self)
			if ok != tt.want || ok && got != peer {
				t.Fatalf("accepted=%v identity=%v, want accepted=%v", ok, got, tt.want)
			}
		})
	}
}

func TestLANDiscoveryPublishedDNSRoundTrip(t *testing.T) {
	self, peer := lanDevice(t, 445), lanDevice(t, 460)
	selected := lanInterface{iface: net.Interface{Index: 3}, ip: net.IPv4(192, 168, 2, 3)}
	zone, err := lanMDNSService(&Credentials{Device: peer}, selected)
	if err != nil {
		t.Fatal(err)
	}
	question := dns.Question{Name: lanService + ".local.", Qtype: dns.TypePTR, Qclass: dns.ClassINET}
	message := &dns.Msg{Answer: zone.Records(question)}
	wire, err := message.Pack()
	if err != nil {
		t.Fatal(err)
	}
	var received dns.Msg
	if err := received.Unpack(wire); err != nil {
		t.Fatal(err)
	}
	entries := entriesFromLANResponse(&received)
	if len(entries) != 1 {
		t.Fatalf("advertisement did not survive DNS encoding: %+v", entries)
	}
	fields := map[string]string{}
	for _, txt := range entries[0].InfoFields {
		if len(txt) >= 8 && txt[:8] == "version=" {
			fields["version"] = txt[8:]
		}
	}
	if fields["version"] != "1" {
		t.Fatalf("version missing from DNS TXT: %v", entries[0].InfoFields)
	}
	if got, ip, ok := parseLANEntry(entries[0], self); !ok || got != peer || !ip.Equal(selected.ip) {
		t.Fatalf("published discovery was rejected: %v %v %v", got, ip, ok)
	}
	// Updated advertisements still have exactly the principal/mesh values
	// expected by old readers. The additive version field does not alter them.
	legacy := *entries[0]
	legacy.InfoFields = legacy.InfoFields[:2]
	if got, _, ok := parseLANEntry(&legacy, self); !ok || got != peer {
		t.Fatal("legacy principal/mesh contract changed")
	}
}

func TestLANMeshHintUnchangedVector(t *testing.T) {
	tenant, err := meshid.ParseTenant("5b8c6c8e-2f9f-4b4a-9a4f-6e2d8c9a0b12")
	if err != nil {
		t.Fatal(err)
	}
	if got := lanMeshHash(tenant); got != "8e6d83e9d13ffe98" {
		t.Fatalf("LAN wire hint changed: %s", got)
	}
}
