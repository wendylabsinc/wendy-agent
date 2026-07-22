package discovery

import "testing"

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
