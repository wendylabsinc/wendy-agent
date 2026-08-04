package discovery

import (
	"reflect"
	"testing"
)

// txtWire encodes entries into DNS-SD TXT wire format.
func txtWire(entries ...string) []byte {
	var out []byte
	for _, e := range entries {
		out = append(out, byte(len(e)))
		out = append(out, e...)
	}
	return out
}

func TestParseTXTRecord(t *testing.T) {
	cases := []struct {
		name string
		txt  []byte
		want map[string]string
	}{
		{name: "empty", txt: nil, want: map[string]string{}},
		{
			name: "key=value pairs",
			txt:  txtWire("tls=true", "assetid=338"),
			want: map[string]string{"tls": "true", "assetid": "338"},
		},
		{
			// The reason this parser exists: dns-sd's display format escaped
			// spaces as "\ " and the old code had to undo that by hand.
			name: "value containing spaces is preserved verbatim",
			txt:  txtWire("displayname=Tom Rpi4"),
			want: map[string]string{"displayname": "Tom Rpi4"},
		},
		{
			name: "value containing an equals sign keeps it",
			txt:  txtWire("token=abc=def"),
			want: map[string]string{"token": "abc=def"},
		},
		{
			name: "attribute with no value maps to empty string",
			txt:  txtWire("standalone"),
			want: map[string]string{"standalone": ""},
		},
		{
			name: "first occurrence of a repeated key wins (RFC 6763 6.4)",
			txt:  txtWire("dup=first", "dup=second"),
			want: map[string]string{"dup": "first"},
		},
		{
			name: "entry with an empty key is skipped",
			txt:  txtWire("=orphaned", "tls=true"),
			want: map[string]string{"tls": "true"},
		},
		{
			name: "zero length byte ends parsing",
			txt:  append(txtWire("tls=true"), 0x00, 'x'),
			want: map[string]string{"tls": "true"},
		},
		{
			name: "length overrunning the buffer keeps what was decoded",
			txt:  append(txtWire("tls=true"), 0x40, 'a', 'b'),
			want: map[string]string{"tls": "true"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseTXTRecord(tc.txt); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseTXTRecord(%v) = %v, want %v", tc.txt, got, tc.want)
			}
		})
	}
}

func TestPreferIPv4Addr(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		want  string
	}{
		{name: "empty", addrs: nil, want: ""},
		{
			name:  "IPv4 preferred over an earlier IPv6",
			addrs: []string{"2600:1011:a003:4221:be41:6859:13c0:f7", "192.168.0.159"},
			want:  "192.168.0.159",
		},
		{
			name:  "first IPv4 wins",
			addrs: []string{"192.168.0.159", "10.0.0.5"},
			want:  "192.168.0.159",
		},
		{
			name:  "falls back to first address when no IPv4",
			addrs: []string{"2001:db8::1", "2001:db8::2"},
			want:  "2001:db8::1",
		},
		{
			name:  "unparseable entries are skipped for the IPv4 scan",
			addrs: []string{"not-an-ip", "192.168.0.159"},
			want:  "192.168.0.159",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := preferIPv4Addr(tc.addrs); got != tc.want {
				t.Errorf("preferIPv4Addr(%v) = %q, want %q", tc.addrs, got, tc.want)
			}
		})
	}
}
