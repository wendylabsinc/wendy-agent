//go:build windows

package discovery

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestParseNetAdapterJSONFiltersWendyByName(t *testing.T) {
	// Single entry: PowerShell emits an object, not an array, in this case.
	in := `{"Name":"Wendy USB Ethernet","InterfaceDescription":"Realtek USB GbE","MacAddress":"00-11-22-33-44-55","LinkSpeed":"1 Gbps","IPAddress":"169.254.1.5"}`

	got := parseNetAdapterJSON(in)
	if len(got) != 1 {
		t.Fatalf("got %d devices, want 1", len(got))
	}
	want := models.EthernetInterface{
		Name:          "Wendy USB Ethernet",
		DisplayName:   "Realtek USB GbE",
		MACAddress:    "00-11-22-33-44-55",
		IPAddress:     "169.254.1.5",
		LinkSpeed:     "1 Gbps",
		IsWendyDevice: true,
	}
	if got[0] != want {
		t.Fatalf("got %#v, want %#v", got[0], want)
	}
}

func TestParseNetAdapterJSONFiltersWendyByDescription(t *testing.T) {
	in := `[
		{"Name":"Ethernet 3","InterfaceDescription":"Wendy Gadget Mode","MacAddress":"AA-BB-CC-DD-EE-FF","LinkSpeed":"100 Mbps","IPAddress":"169.254.2.7"},
		{"Name":"Wi-Fi","InterfaceDescription":"Intel AX201","MacAddress":"11-22-33-44-55-66","LinkSpeed":"866 Mbps","IPAddress":"192.168.0.20"}
	]`

	got := parseNetAdapterJSON(in)
	if len(got) != 1 {
		t.Fatalf("got %d devices, want 1 (case-insensitive Wendy match on InterfaceDescription)", len(got))
	}
	if got[0].Name != "Ethernet 3" || got[0].DisplayName != "Wendy Gadget Mode" {
		t.Fatalf("unexpected entry: %#v", got[0])
	}
}

func TestParseNetAdapterJSONIsCaseInsensitive(t *testing.T) {
	in := `{"Name":"WENDY ADAPTER","InterfaceDescription":"some vendor","MacAddress":"","LinkSpeed":"","IPAddress":""}`
	got := parseNetAdapterJSON(in)
	if len(got) != 1 {
		t.Fatalf("got %d devices, want 1 (uppercase WENDY should match)", len(got))
	}
}

func TestParseNetAdapterJSONEmpty(t *testing.T) {
	if got := parseNetAdapterJSON(""); got != nil {
		t.Fatalf("parseNetAdapterJSON(\"\") = %#v, want nil", got)
	}
	if got := parseNetAdapterJSON("   \n  "); got != nil {
		t.Fatalf("parseNetAdapterJSON(whitespace) = %#v, want nil", got)
	}
}

func TestParseNetAdapterJSONIgnoresNonWendy(t *testing.T) {
	in := `[
		{"Name":"Ethernet","InterfaceDescription":"Realtek PCIe","MacAddress":"00-00-00-00-00-00","LinkSpeed":"1 Gbps","IPAddress":"192.168.1.10"},
		{"Name":"vEthernet (Default Switch)","InterfaceDescription":"Hyper-V Virtual Ethernet Adapter","MacAddress":"","LinkSpeed":"10 Gbps","IPAddress":"172.20.0.1"}
	]`
	if got := parseNetAdapterJSON(in); len(got) != 0 {
		t.Fatalf("got %d devices, want 0 (no Wendy match)", len(got))
	}
}

func TestParseNetAdapterJSONMalformedReturnsNil(t *testing.T) {
	if got := parseNetAdapterJSON("{not json"); got != nil {
		t.Fatalf("malformed JSON returned %#v, want nil", got)
	}
}
