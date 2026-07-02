package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestFormatNetworkInterfaces(t *testing.T) {
	ifaces := []*agentpb.NetworkInterface{
		{Name: "eth0", IpAddresses: []string{"192.168.1.42"}},
		{Name: "wlan0", IpAddresses: []string{"10.0.0.5", "2001:db8::1"}},
		{Name: "empty"},
	}

	got := formatNetworkInterfaces(ifaces)

	if !strings.HasPrefix(got, "Network:\n") {
		t.Errorf("output should start with a Network: header, got:\n%s", got)
	}
	if !strings.Contains(got, "eth0") || !strings.Contains(got, "192.168.1.42") {
		t.Errorf("output missing eth0 address:\n%s", got)
	}
	if !strings.Contains(got, "10.0.0.5, 2001:db8::1") {
		t.Errorf("output should join multiple addresses with a comma:\n%s", got)
	}
	if strings.Contains(got, "empty") {
		t.Errorf("interfaces without addresses should be skipped:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("output should end with a newline:\n%q", got)
	}
}

func TestBestReachableIP(t *testing.T) {
	cases := []struct {
		name   string
		ifaces []*agentpb.NetworkInterface
		want   string
	}{
		{
			name:   "none",
			ifaces: nil,
			want:   "",
		},
		{
			name: "prefers IPv4 over an earlier IPv6",
			ifaces: []*agentpb.NetworkInterface{
				{Name: "eth0", IpAddresses: []string{"2001:db8::1", "192.168.1.42"}},
			},
			want: "192.168.1.42",
		},
		{
			name: "first IPv4 across interfaces wins",
			ifaces: []*agentpb.NetworkInterface{
				{Name: "eth0", IpAddresses: []string{"192.168.1.42"}},
				{Name: "wlan0", IpAddresses: []string{"10.0.0.5"}},
			},
			want: "192.168.1.42",
		},
		{
			name: "falls back to IPv6 when no IPv4 exists",
			ifaces: []*agentpb.NetworkInterface{
				{Name: "eth0", IpAddresses: []string{"2001:db8::1"}},
			},
			want: "2001:db8::1",
		},
		{
			name: "ignores unparseable addresses",
			ifaces: []*agentpb.NetworkInterface{
				{Name: "eth0", IpAddresses: []string{"not-an-ip", "192.168.1.42"}},
			},
			want: "192.168.1.42",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bestReachableIP(tc.ifaces); got != tc.want {
				t.Errorf("bestReachableIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReachableAppURL(t *testing.T) {
	tcpReadiness := &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: 3001}}

	cases := []struct {
		name      string
		hookURL   string
		appID     string
		deviceIP  string
		readiness *appconfig.ReadinessConfig
		want      string
	}{
		{
			name:     "no device IP yields nothing",
			hookURL:  "http://${WENDY_HOSTNAME}:3001",
			deviceIP: "",
			want:     "",
		},
		{
			name:     "hook URL with hostname placeholder gets the IP swapped in",
			hookURL:  "http://${WENDY_HOSTNAME}:3001/dashboard",
			deviceIP: "192.168.1.42",
			want:     "http://192.168.1.42:3001/dashboard",
		},
		{
			name:     "windows-style placeholder is also rewritten",
			hookURL:  "http://%WENDY_HOSTNAME%:8080",
			deviceIP: "192.168.1.42",
			want:     "http://192.168.1.42:8080",
		},
		{
			name:      "hook URL without a hostname placeholder falls back to readiness port",
			hookURL:   "http://localhost:9999",
			deviceIP:  "192.168.1.42",
			readiness: tcpReadiness,
			want:      "http://192.168.1.42:3001",
		},
		{
			name:      "no hook URL uses readiness TCP port over http",
			deviceIP:  "192.168.1.42",
			readiness: tcpReadiness,
			want:      "http://192.168.1.42:3001",
		},
		{
			name:      "IPv6 address is bracketed",
			deviceIP:  "2001:db8::1",
			readiness: tcpReadiness,
			want:      "http://[2001:db8::1]:3001",
		},
		{
			name:     "no hook URL and no readiness port yields nothing",
			deviceIP: "192.168.1.42",
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reachableAppURL(tc.hookURL, tc.appID, tc.deviceIP, tc.readiness)
			if got != tc.want {
				t.Errorf("reachableAppURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
