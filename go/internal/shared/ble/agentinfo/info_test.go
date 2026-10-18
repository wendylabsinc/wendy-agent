package agentinfo

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func validInfo() Info {
	return Info{ProtocolVersion: ProtocolVersion, PSM: DefaultPSM, Identity: "urn:wendy:org:7:asset:42", Hostname: "wendy-pi", Enrolled: true, Capabilities: []string{"wifi", "apps"}}
}

func TestInfoRoundTrip(t *testing.T) {
	want := validInfo()
	encoded, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// Additional fields must not break compatible future metadata.
	encoded = bytes.Replace(encoded, []byte(`"psm":128`), []byte(`"psm":128,"future":true`), 1)
	if _, err := Decode(encoded); err != nil {
		t.Fatal(err)
	}
}

func TestInfoRejectsUnusableOrMalformedMetadata(t *testing.T) {
	tests := map[string]func(*Info){
		"future protocol":    func(i *Info) { i.ProtocolVersion++ },
		"fixed PSM":          func(i *Info) { i.PSM = 1 },
		"out of range PSM":   func(i *Info) { i.PSM = 256 },
		"unenrolled":         func(i *Info) { i.Enrolled = false },
		"user identity":      func(i *Info) { i.Identity = "urn:wendy:org:7:user:42" },
		"empty identity":     func(i *Info) { i.Identity = "" },
		"empty hostname":     func(i *Info) { i.Hostname = "" },
		"control hostname":   func(i *Info) { i.Hostname = "pi\nforged" },
		"empty capabilities": func(i *Info) { i.Capabilities = nil },
		"invalid capability": func(i *Info) { i.Capabilities = []string{"wifi\n"} },
	}
	for name, modify := range tests {
		t.Run(name, func(t *testing.T) {
			info := validInfo()
			modify(&info)
			data, err := json.Marshal(info)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(data); err == nil {
				t.Fatal("accepted invalid metadata")
			}
			if _, err := Encode(info); err == nil {
				t.Fatal("published invalid metadata")
			}
		})
	}
	for _, raw := range [][]byte{nil, []byte("null"), []byte("{"), bytes.Repeat([]byte(" "), MaxSize+1)} {
		if _, err := Decode(raw); err == nil {
			t.Fatalf("accepted invalid input of size %d", len(raw))
		}
	}
}

func TestInfoEnforcesCharacteristicSize(t *testing.T) {
	info := validInfo()
	info.Hostname = strings.Repeat("h", 253)
	info.Identity = "spiffe://wendy.sh/tenant/6a2fe898-50e7-4a68-a747-65d8d06ae271/device/" + strings.Repeat("d", 200)
	if _, err := Encode(info); err == nil {
		t.Fatal("accepted payload larger than GATT characteristic limit")
	}
}
