package usermessage

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Review 4339052670: an agent's missing resource does not imply a missing device.
func TestReviewResourceNotFoundKeepsContext(t *testing.T) {
	for _, resource := range []string{"app typo", "camera 3", "bag missing", "volume cache"} {
		got := Explain(fmt.Errorf("looking up %s: %w", resource, status.Error(codes.NotFound, "private resource lookup")), "").Text()
		if !strings.Contains(got, resource) || strings.Contains(got, NotFound) || strings.Contains(got, "private resource") {
			t.Errorf("wrong resource explanation: %q", got)
		}
	}
}

// Review 4339052682: HTTPS trust failures are not device clock evidence.
func TestReviewHTTPSCertificateFailure(t *testing.T) {
	err := fmt.Errorf("downloading template: %w", &url.Error{Op: "Get", URL: "https://example.test", Err: x509.UnknownAuthorityError{}})
	got := Explain(err, "").Text()
	if !strings.Contains(got, "Downloading template:") || !strings.Contains(got, "secure connection") || strings.Contains(got, "sync-time") || strings.Contains(got, "device rejected") || strings.Contains(got, "x509:") {
		t.Errorf("wrong HTTPS guidance: %q", got)
	}
}

// Review 4339052709: replace the foreign leaf while preserving authored context.
func TestReviewAuthoredWrapperAndWorkaroundSurvive(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   []string
	}{
		{"registering deployment with Cloud: %w; use --skip-cloud-registration for an offline deployment", []string{"Registering deployment with Cloud:", "Wendy Cloud is unavailable.", "Use --skip-cloud-registration"}},
		{"default device my-pi is set but could not be reached: %w", []string{"Default device my-pi is set but could not be reached:", "unavailable."}},
	} {
		got := Explain(fmt.Errorf(tc.format, status.Error(codes.Unavailable, "private restart detail")), "").Text()
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("lost %q: %q", want, got)
			}
		}
		if strings.Contains(got, "private restart") || strings.Contains(got, "rpc error") {
			t.Errorf("foreign text leaked: %q", got)
		}
	}
	joined := errors.Join(errors.New("local sibling"), fmt.Errorf("fetching apps: %w", status.Error(codes.Internal, "private cause")))
	if got := Text(joined); !strings.Contains(got, "Local sibling") || !strings.Contains(got, "fetching apps:") || strings.Contains(got, "private cause") {
		t.Errorf("lost joined context: %q", got)
	}
}

func TestForeignReplacementHandlesRepeatedAndOverlappingCauses(t *testing.T) {
	cause := status.Error(codes.Internal, "private diagnostic")
	repeated := fmt.Errorf("operation: %w; repeated failure: %s", cause, cause)
	overlap := errors.Join(Foreign(errors.New("private")), Foreign(errors.New("private extended")))
	adjacent := fmt.Errorf("operation: %w; %w", Foreign(errors.New("first body")), Foreign(errors.New("second body")))
	for _, err := range []error{repeated, overlap, adjacent} {
		got := Text(err)
		if strings.Contains(got, "private") || strings.Contains(got, "extended") || strings.Contains(got, "rpc error") || strings.Contains(got, "body") || strings.Count(strings.ToLower(got), strings.ToLower(Generic)) != 2 {
			t.Errorf("incomplete foreign replacement: %q", got)
		}
	}
}

func TestInlineRecoveryCommandIsNeitherPunctuatedNorDuplicated(t *testing.T) {
	step := "Pass --force to remove: wendy device apps remove demo --force"
	err := advisedError{cause: errors.New("refusing removal"), steps: []string{step}}
	if got := Sprintf("%v\n", err); got != "Refusing removal.\n  "+step+"\n" {
		t.Errorf("recovery command changed: %q", got)
	}
}
