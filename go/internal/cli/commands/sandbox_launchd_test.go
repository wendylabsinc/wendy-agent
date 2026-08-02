package commands

import (
	"strings"
	"testing"
)

func TestRenderSandboxPlist_SubstitutesAllFields(t *testing.T) {
	xml, err := renderSandboxPlist(sandboxPlistParams{
		Label: "sh.wendy.sandbox-control-plane", WorkDir: "/tmp/cp", LogPath: "/tmp/cp.log",
		Port: "8787", AdminUser: "admin", AdminPassword: "s3cr3t", DataDir: "/tmp/cp-data",
	})
	if err != nil {
		t.Fatalf("renderSandboxPlist: %v", err)
	}
	for _, want := range []string{
		"<string>sh.wendy.sandbox-control-plane</string>",
		"<string>/tmp/cp</string>",
		"<string>/tmp/cp.log</string>",
		"<string>8787</string>",
		"<string>admin</string>",
		"<string>s3cr3t</string>",
		"<string>/tmp/cp-data</string>",
		"<key>KeepAlive</key>",
		"<true/>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("rendered plist missing %q\nfull output:\n%s", want, xml)
		}
	}
}

func TestRenderSandboxPlist_EscapesXMLSpecialCharacters(t *testing.T) {
	xml, err := renderSandboxPlist(sandboxPlistParams{
		Label: "sh.wendy.sandbox-control-plane", WorkDir: "/tmp/cp", LogPath: "/tmp/cp.log",
		Port: "8787", AdminUser: "admin", AdminPassword: `a&b<c>d`, DataDir: "/tmp/cp-data",
	})
	if err != nil {
		t.Fatalf("renderSandboxPlist: %v", err)
	}
	if strings.Contains(xml, "a&b<c>d") {
		t.Error("rendered plist contains un-escaped XML special characters in the password")
	}
	if !strings.Contains(xml, "a&amp;b&lt;c&gt;d") {
		t.Errorf("rendered plist missing escaped password\nfull output:\n%s", xml)
	}
}
