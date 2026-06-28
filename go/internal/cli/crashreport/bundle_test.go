package crashreport

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

func TestBuildRedactsAndBounds(t *testing.T) {
	long := make([]string, 500)
	for i := range long {
		long[i] = "line"
	}
	b := Build(platforminfo.Info{CLIVersion: "0.1"}, "grpc_other", "unrecoverable",
		"deploy: dial 10.0.0.5: refused", []string{"connect a@b.com"}, long)

	if strings.Contains(b.ErrorChain, "10.0.0.5") {
		t.Errorf("error chain not redacted: %q", b.ErrorChain)
	}
	if len(b.BuildOutputTail) != 200 {
		t.Errorf("build tail not bounded: %d", len(b.BuildOutputTail))
	}
	if strings.Contains(strings.Join(b.LogTail, "\n"), "a@b.com") {
		t.Errorf("log tail not redacted: %v", b.LogTail)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	b := Build(platforminfo.Info{CLIVersion: "0.1"}, "other", "unrecoverable", "boom", nil, nil)
	req := b.Request()
	if req.GetSeverity() != "unrecoverable" || req.GetErrorChain() != "boom" {
		t.Errorf("request mismatch: %+v", req)
	}
	if req.GetPlatformInfo().GetCliVersion() != "0.1" {
		t.Errorf("platform info missing")
	}
}

func TestValidTrackingID(t *testing.T) {
	cases := map[string]bool{"WDY-7Q4ZK2": true, "wdy-7q4zk2": false, "WDY-12345": false, "": false}
	for id, want := range cases {
		if ValidTrackingID(id) != want {
			t.Errorf("ValidTrackingID(%q) = %v, want %v", id, !want, want)
		}
	}
}
