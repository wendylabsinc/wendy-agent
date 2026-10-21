package usermessage

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"unicode"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func assertSentences(t *testing.T, text string) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		chars := []rune(line)
		if !unicode.IsUpper(chars[0]) || !strings.ContainsRune(".?!", chars[len(chars)-1]) {
			t.Errorf("not a sentence: %q", line)
		}
	}
}

func TestExplainKnownFailures(t *testing.T) {
	for _, tc := range []struct {
		code      codes.Code
		raw, want string
	}{
		{codes.Unavailable, "device is offline", Offline},
		{codes.Unavailable, "asset not online", Offline},
		{codes.NotFound, "asset not found", NotFound},
		{codes.Unimplemented, "private feature details", "Not supported by this agent version. Try updating the agent."},
		{codes.PermissionDenied, "private permission detail", "You don't have permission to do this. Check your access and try again."},
		{codes.Unauthenticated, "private credential detail", "Sign in again to continue."},
		{codes.InvalidArgument, "invalid internal field", "The requested settings are not valid. Check them and try again."},
		{codes.OutOfRange, "value 1234 outside range", "The requested settings are not valid. Check them and try again."},
		{codes.AlreadyExists, "asset 123 exists", "This already exists. Choose a different name and try again."},
		{codes.ResourceExhausted, "quota xyz 123", "The device or service has reached its limit. Free resources and try again."},
		{codes.FailedPrecondition, "internal precondition", "The device is not ready for this action. Check its state and try again."},
		{codes.Aborted, "write transaction aborted", "The action was interrupted. Try again."},
		{codes.Canceled, "context canceled", "The action was cancelled."},
		{codes.DeadlineExceeded, "transport deadline hit", "Connection timed out."},
		{codes.Internal, "private stack trace", Generic},
		{codes.DataLoss, "data checksum xyz", Generic},
		{codes.Unknown, "unrecognized private reason", Generic},
	} {
		for _, err := range []error{status.Error(tc.code, tc.raw), fmt.Errorf("raw wrapper: %w\nraw suffix", status.Error(tc.code, tc.raw))} {
			r := Explain(err, "")
			if r.Message != tc.want {
				t.Errorf("%s: %q, want %q", tc.code, r.Message, tc.want)
			}
			assertSentences(t, r.Text())
			for _, raw := range []string{tc.raw, "raw wrapper", "raw suffix", "rpc error:"} {
				if strings.Contains(r.Text(), raw) {
					t.Errorf("leaked %q in %q", raw, r.Text())
				}
			}
		}
	}
}

func TestExplainUnknownTransportAndWrappedChains(t *testing.T) {
	for _, raw := range []string{"asset not found", "device is offline", "rpc error: code = Unavailable desc = device is offline", "rpc error: code = Unknown desc = secret server detail", "http 500: secret body", "lowercase library text", "\x1b[31mforged terminal text\x1b[0m"} {
		for _, err := range []error{errors.New(raw), fmt.Errorf("raw context: %w", errors.New(raw)), errors.Join(errors.New(raw), errors.New("raw sibling"))} {
			got := Explain(err, "").Text()
			assertSentences(t, got)
			for _, secret := range []string{raw, "raw context", "raw sibling"} {
				if strings.Contains(got, secret) {
					t.Errorf("leaked %q in %q", secret, got)
				}
			}
		}
	}
	for _, err := range []error{
		&net.OpError{Op: "dial", Err: errors.New("raw socket detail")},
		&net.DNSError{Err: "raw lookup detail", Name: "private host"},
		&tls.CertificateVerificationError{Err: errors.New("raw certificate detail")},
		&url.Error{Op: http.MethodGet, URL: "https://private.host/token", Err: errors.New("raw body")},
		&os.PathError{Op: "open", Path: "private file", Err: os.ErrPermission},
		context.DeadlineExceeded,
	} {
		got := Explain(fmt.Errorf("raw wrapper: %w", err), "").Text()
		assertSentences(t, got)
		if strings.Contains(got, "raw") || strings.Contains(got, "private") {
			t.Errorf("leaked %q", got)
		}
	}
}

func TestExplainAllCommandCategories(t *testing.T) {
	for _, category := range []string{"memory_ambiguous", "device_identity_mismatch", "auth_required", "device_auth_required", "auth_session_ambiguous", "cli_usage", "config_invalid", "no_device", "device_ambiguous", "device_org_mismatch", "project_target_mismatch", "build_failed", "builder_unavailable", "transfer_failed", "container_start_failed", "readiness_timeout", "app_crashed", "registry_auth", "registry_unavailable", "tool_not_found", "disk_full", "device_offline"} {
		got := Explain(errors.New("private cause"), category).Text()
		assertSentences(t, got)
		if got == Generic || strings.Contains(got, "private cause") {
			t.Errorf("unmapped category %s: %q", category, got)
		}
	}
}

func TestTunnelRefusalUsesApprovedReason(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"PermissionDenied desc = private membership detail", "You don't have permission to do this. Check your access and try again."},
		{"NotFound desc = asset not found", NotFound},
		{"Unavailable desc = asset not online", Offline},
	} {
		err := status.Error(codes.Unavailable, `transport: authentication handshake failed: cloud tunnel closed by broker: rpc error: code = `+tc.raw)
		if got := Explain(err, "").Message; got != tc.want {
			t.Errorf("message = %q, want %q", got, tc.want)
		}
	}
}

func TestInlineMessagesMapErrorArguments(t *testing.T) {
	t.Setenv("WENDY_TLS_DEBUG", "")
	got := Sprintf("could not connect: %v\n", status.Error(codes.Unavailable, "device is offline"))
	if got != "Could not connect: Device is offline. Make sure it's on and connected, then try again.\n" {
		t.Errorf("message = %q", got)
	}
	assertSentences(t, got)
	if got := Server("asset not found"); got != NotFound {
		t.Errorf("server failure = %q", got)
	}
}

func TestKnownLocalRefusals(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"enter a unicast IPv4 address for the robot", "Enter a unicast IPv4 address for the robot."},
		{"create the simulator before setting it as default", "Create the simulator before setting it as default."},
		{"network camera management is not supported by Wendy Agent for macOS", "Network camera management is not supported by Wendy Agent for Mac."},
		{"this device's Wendy Agent cannot scan its network for sensor sources; update it", "This device cannot scan its network for sensor sources. Update its agent and try again."},
		{"not authorized to pair with this device", "You don't have permission to pair with this device. Check your access and try again."},
		{"device is not reachable", "Device is not reachable. Make sure it's on and connected, then try again."},
		{"the device did not answer in time", "The device did not answer in time. Check its connection and try again."},
		{"the device did not finish scanning its network in time", "The device did not finish scanning its network in time. Try again."},
	} {
		got := Explain(fmt.Errorf("raw wrapper: %w", errors.New(tc.raw)), "").Text()
		if got != tc.want {
			t.Errorf("message = %q, want %q", got, tc.want)
		}
		assertSentences(t, got)
	}
}

type advisedError struct {
	cause error
	steps []string
}

func (e advisedError) Error() string       { return e.cause.Error() }
func (e advisedError) Unwrap() error       { return e.cause }
func (e advisedError) NextSteps() []string { return e.steps }

func TestAttachedAdviceIsAllowlisted(t *testing.T) {
	err := errors.Join(advisedError{errors.New("raw cause"), []string{"raw server advice", "Confirm it with 'wendy device get-default'."}}, advisedError{errors.New("raw sibling"), []string{"Check its network connection and retry."}})
	got := Explain(err, "")
	if len(got.Steps) != 2 {
		t.Errorf("approved steps = %q", got.Steps)
	}
	if strings.Contains(got.Text(), "raw") {
		t.Errorf("raw advice leaked: %q", got.Text())
	}
	assertSentences(t, got.Text())
}

func TestTypedRefusalCannotBeOverriddenByDescription(t *testing.T) {
	for _, raw := range []string{"asset not found", "device is offline", "rpc error: code = Unavailable desc = device is offline"} {
		got := Explain(status.Error(codes.PermissionDenied, raw), "").Message
		if got != "You don't have permission to do this. Check your access and try again." {
			t.Errorf("refusal became %q", got)
		}
	}
	if got := Explain(status.Error(codes.Unavailable, "device is offline"), "device_identity_mismatch").Message; got != "Connection blocked because the device's identity has changed." {
		t.Errorf("identity refusal became %q", got)
	}
}
