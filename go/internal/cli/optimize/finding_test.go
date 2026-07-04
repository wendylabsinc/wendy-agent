package optimize

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSeverityString(t *testing.T) {
	cases := []struct {
		sev  Severity
		want string
	}{
		{SeverityInfo, "info"},
		{SeverityWarning, "warning"},
		{SeverityError, "error"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := c.sev.String(); got != c.want {
				t.Fatalf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseSeverity(t *testing.T) {
	got, err := ParseSeverity("warning")
	if err != nil {
		t.Fatalf("ParseSeverity returned error: %v", err)
	}
	if got != SeverityWarning {
		t.Fatalf("ParseSeverity = %v, want SeverityWarning", got)
	}
	if _, err := ParseSeverity("bogus"); err == nil {
		t.Fatalf("ParseSeverity(\"bogus\") expected error, got nil")
	}
}

func TestSeverityJSON(t *testing.T) {
	b, err := json.Marshal(SeverityWarning)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"warning"` {
		t.Fatalf("marshal = %s, want \"warning\"", b)
	}
	var s Severity
	if err := json.Unmarshal([]byte(`"error"`), &s); err != nil {
		t.Fatal(err)
	}
	if s != SeverityError {
		t.Fatalf("unmarshal = %v, want SeverityError", s)
	}
	// Finding marshals severity under the lowercase key with a string value.
	fb, err := json.Marshal(Finding{Analyzer: "x", Severity: SeverityError, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fb), `"severity":"error"`) {
		t.Fatalf("finding json = %s, want severity:\"error\"", fb)
	}
}
