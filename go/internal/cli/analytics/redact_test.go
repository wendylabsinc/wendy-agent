package analytics

import (
	"strings"
	"testing"
)

func TestRedactErrorDetail_StripsPII(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		leaks   []string // substrings that MUST NOT survive
		survive []string // structural text that MUST survive
	}{
		{
			name:    "unix home path",
			in:      "opening image: open /Users/joannis/secret/wendyos.img: no such file or directory",
			leaks:   []string{"/Users/joannis", "joannis", "secret", "wendyos.img"},
			survive: []string{"opening image", "no such file or directory"},
		},
		{
			name:    "device node",
			in:      "writing image: /dev/disk4: input/output error",
			leaks:   []string{"/dev/disk4"},
			survive: []string{"writing image", "input/output error"},
		},
		{
			name:    "windows path",
			in:      `creating temp file: open C:\Users\joannis\AppData\wendyos-123.img: access denied`,
			leaks:   []string{`C:\Users\joannis`, "joannis", "AppData"},
			survive: []string{"creating temp file", "access denied"},
		},
		{
			name:    "url",
			in:      "downloading: Get \"https://secret-host.example.com/images/wendyos.zip\": connection refused",
			leaks:   []string{"secret-host.example.com", "secret-host", "/images/wendyos.zip"},
			survive: []string{"downloading", "connection refused"},
		},
		{
			name:    "bare fqdn",
			in:      "connecting to internal-registry.corp.example.net timed out",
			leaks:   []string{"internal-registry.corp.example.net", "internal-registry"},
			survive: []string{"connecting to", "timed out"},
		},
		{
			name:    "ipv4",
			in:      "dial tcp 192.168.1.42:8080: connect: connection refused",
			leaks:   []string{"192.168.1.42"},
			survive: []string{"dial tcp", "connection refused"},
		},
		{
			name:    "ipv6",
			in:      "dial tcp [2001:db8:85a3::8a2e:370:7334]:443: no route to host",
			leaks:   []string{"2001:db8:85a3", "8a2e:370:7334"},
			survive: []string{"dial tcp", "no route to host"},
		},
		{
			name:    "mac address",
			in:      "bluetooth device 00:1a:7d:da:71:13 not found",
			leaks:   []string{"00:1a:7d:da:71:13"},
			survive: []string{"bluetooth device", "not found"},
		},
		{
			name:    "email",
			in:      "auth failed for joannis@wendy.sh: invalid token",
			leaks:   []string{"joannis@wendy.sh", "joannis"},
			survive: []string{"auth failed", "invalid token"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactErrorDetail(tc.in)
			for _, leak := range tc.leaks {
				if strings.Contains(got, leak) {
					t.Errorf("redacted output leaked %q\n  in:  %q\n  got: %q", leak, tc.in, got)
				}
			}
			for _, s := range tc.survive {
				if !strings.Contains(got, s) {
					t.Errorf("redacted output dropped structural text %q\n  in:  %q\n  got: %q", s, tc.in, got)
				}
			}
		})
	}
}

func TestRedactErrorDetail_PreservesSafeTokens(t *testing.T) {
	in := "version 0.10.4 not found for raspberry-pi-5"
	got := RedactErrorDetail(in)
	for _, want := range []string{"0.10.4", "raspberry-pi-5", "not found"} {
		if !strings.Contains(got, want) {
			t.Errorf("safe token %q was redacted: %q", want, got)
		}
	}
}

func TestRedactErrorDetail_CapsLength(t *testing.T) {
	in := strings.Repeat("abcdefghij ", 100) // 1100 runes, no PII
	got := RedactErrorDetail(in)
	if len([]rune(got)) > maxErrorDetailRunes {
		t.Errorf("redacted output length = %d runes, want <= %d", len([]rune(got)), maxErrorDetailRunes)
	}
}

func TestRedactErrorDetail_Empty(t *testing.T) {
	if got := RedactErrorDetail(""); got != "" {
		t.Errorf("RedactErrorDetail(\"\") = %q, want \"\"", got)
	}
}
