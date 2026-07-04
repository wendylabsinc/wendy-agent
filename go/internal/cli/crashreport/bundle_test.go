package crashreport

import (
	"encoding/json"
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

func TestValidTrackingID(t *testing.T) {
	cases := map[string]bool{"WDY-7Q4ZK2": true, "wdy-7q4zk2": false, "WDY-12345": false, "": false}
	for id, want := range cases {
		if ValidTrackingID(id) != want {
			t.Errorf("ValidTrackingID(%q) = %v, want %v", id, !want, want)
		}
	}
}

func TestBundlePayloadJSON(t *testing.T) {
	b := Build(
		platforminfo.Info{CLIVersion: "1.2.3", DevOS: "darwin"},
		"other", "unrecoverable", "boom",
		[]string{"line1"}, nil,
	)
	p := b.Payload("anon-123", true)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{`"anonymous_id":"anon-123"`, `"notify_on_fix":true`, `"error_class":"other"`, `"cli_version":"1.2.3"`, `"error_chain":"boom"`} {
		if !strings.Contains(s, want) {
			t.Errorf("payload missing %s: %s", want, s)
		}
	}
}
