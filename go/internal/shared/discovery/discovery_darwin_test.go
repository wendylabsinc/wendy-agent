//go:build darwin

package discovery

import (
	"reflect"
	"testing"
)

func TestParseBrowseLine(t *testing.T) {
	// Interface index 9999 never resolves, so interfaceName is deterministically
	// empty regardless of the machine's network configuration.
	tests := []struct {
		name   string
		line   string
		want   browseResult
		wantOK bool
	}{
		{
			name:   "valid add line",
			line:   "14:05:31.123  Add        3   9999 local.               _wendy._tcp.         wendy-device",
			want:   browseResult{instanceName: "wendy-device", domain: "local.", interfaceName: ""},
			wantOK: true,
		},
		{
			name:   "multi-word instance name",
			line:   "14:05:31.123  Add        3   9999 local.               _wendy._tcp.         Dynamic Cosmos",
			want:   browseResult{instanceName: "Dynamic Cosmos", domain: "local.", interfaceName: ""},
			wantOK: true,
		},
		{
			name:   "remove line",
			line:   "14:05:31.123  Rmv        2   9999 local.               _wendy._tcp.         wendy-device",
			wantOK: false,
		},
		{
			name:   "header line",
			line:   "Timestamp     A/R    Flags  if Domain               Service Type         Instance Name",
			wantOK: false,
		},
		{
			name:   "too few fields",
			line:   "14:05:31.123  Add        3   9999 local.",
			wantOK: false,
		},
		{
			name:   "empty line",
			line:   "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseBrowseLine(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parseBrowseLine(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("parseBrowseLine(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

func TestParseDNSSDTXT(t *testing.T) {
	tests := []struct {
		name string
		line string
		want map[string]string
	}{
		{
			name: "plain pairs",
			line: " tls=true id=abc123",
			want: map[string]string{"tls": "true", "id": "abc123"},
		},
		{
			name: "escaped space in value",
			line: ` displayname=Dynamic\ Cosmos tls=true`,
			want: map[string]string{"displayname": "Dynamic Cosmos", "tls": "true"},
		},
		{
			name: "tokens without equals are ignored",
			line: "some noise displayname=wendy more noise",
			want: map[string]string{"displayname": "wendy"},
		},
		{
			name: "empty line",
			line: "",
			want: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make(map[string]string)
			parseDNSSDTXT(tt.line, got)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseDNSSDTXT(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}
