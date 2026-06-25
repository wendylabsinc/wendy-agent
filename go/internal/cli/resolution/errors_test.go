package resolution

import (
	"errors"
	"testing"
)

func TestResolutionErrorError_AllFourSources(t *testing.T) {
	err := &ResolutionError{
		Target: "device.local",
		SourceResults: map[Source]string{
			SourceLiteralIP: "not an IP",
			SourceMDNS:      "2 candidate(s) from mDNS",
			SourceDNS:       "skipped (.local hostname)",
			SourceCache:     "no cached endpoint",
		},
	}

	output := err.Error()
	expected := `could not reach "device.local":
  literal-ip: not an IP
  mdns:       2 candidate(s) from mDNS
  dns:        skipped (.local hostname)
  cache:      no cached endpoint`

	if output != expected {
		t.Errorf("unexpected output:\ngot:\n%s\n\nexpected:\n%s", output, expected)
	}
}

func TestResolutionErrorError_PartialMap(t *testing.T) {
	err := &ResolutionError{
		Target: "192.168.1.1",
		SourceResults: map[Source]string{
			SourceMDNS:  "no response",
			SourceCache: "no cached endpoint",
		},
	}

	output := err.Error()
	expected := `could not reach "192.168.1.1":
  mdns:  no response
  cache: no cached endpoint`

	if output != expected {
		t.Errorf("unexpected output:\ngot:\n%s\n\nexpected:\n%s", output, expected)
	}
}

func TestResolutionErrorAs(t *testing.T) {
	err := &ResolutionError{
		Target: "test.local",
		SourceResults: map[Source]string{
			SourceMDNS: "no response",
			SourceDNS:  "DNS error: i/o timeout",
		},
	}

	var resErr *ResolutionError
	if !errors.As(err, &resErr) {
		t.Fatal("errors.As failed to unwrap ResolutionError")
	}

	if resErr.Target != "test.local" {
		t.Errorf("expected Target='test.local', got %q", resErr.Target)
	}

	if resErr.SourceResults[SourceMDNS] != "no response" {
		t.Errorf("expected SourceResults[SourceMDNS]='no response', got %q", resErr.SourceResults[SourceMDNS])
	}
}
