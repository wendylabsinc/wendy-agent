package usermessage

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"unicode"

	"github.com/spf13/pflag"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func assertSentences(t *testing.T, text string) {
	t.Helper()
	text = strings.TrimSpace(text)
	chars := []rune(text)
	if len(chars) == 0 || !unicode.IsUpper(chars[0]) || !strings.ContainsRune(".?!", chars[len(chars)-1]) {
		t.Errorf("not a sentence: %q", text)
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
		for _, input := range []struct {
			err  error
			want string
		}{
			{status.Error(tc.code, tc.raw+"; private server diagnostic"), tc.want},
			{fmt.Errorf("listing resources: %w\n  Retry later.", status.Error(tc.code, tc.raw+"; private server diagnostic")), "Listing resources: " + strings.ToLower(tc.want[:1]) + tc.want[1:] + "\n  Retry later."},
		} {
			r := Explain(input.err, "")
			if r.Message != input.want {
				t.Errorf("%s: %q, want %q", tc.code, r.Message, input.want)
			}
			assertSentences(t, r.Text())
			for _, raw := range []string{"private server diagnostic", "rpc error:"} {
				if strings.Contains(r.Text(), raw) {
					t.Errorf("leaked %q in %q", raw, r.Text())
				}
			}
		}
	}
}

func TestExplainUnknownTransportAndWrappedChains(t *testing.T) {
	for _, raw := range []string{"asset not found", "device is offline", "rpc error: code = Unavailable desc = device is offline", "rpc error: code = Unknown desc = secret server detail", "http 500: secret body", "lowercase library text", "\x1b[31mforged terminal text\x1b[0m"} {
		for _, err := range []error{Foreign(errors.New(raw)), Foreign(fmt.Errorf("raw context: %w", errors.New(raw))), errors.Join(Foreign(errors.New(raw)), Foreign(errors.New("raw sibling")))} {
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
		context.DeadlineExceeded,
	} {
		got := Explain(fmt.Errorf("local operation: %w", err), "").Text()
		assertSentences(t, got)
		if strings.Contains(got, "raw") || strings.Contains(got, "private") {
			t.Errorf("leaked %q", got)
		}
	}
}

func TestExplainForeignCommandCategories(t *testing.T) {
	for _, category := range []string{"auth_required", "device_auth_required", "auth_session_ambiguous", "cli_usage", "config_invalid", "no_device", "device_ambiguous", "device_org_mismatch", "project_target_mismatch", "build_failed", "builder_unavailable", "transfer_failed", "container_start_failed", "readiness_timeout", "app_crashed", "registry_auth", "registry_unavailable", "tool_not_found", "disk_full", "device_offline"} {
		got := Explain(Foreign(errors.New("private cause")), category).Text()
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
	if got != "Could not connect: device is offline. Make sure it's on and connected, then try again.\n" {
		t.Errorf("message = %q", got)
	}
	assertSentences(t, got)
	if got := Server("asset not found"); got != NotFound {
		t.Errorf("server failure = %q", got)
	}
}

func TestKnownForeignReasons(t *testing.T) {
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
		got := Explain(Foreign(fmt.Errorf("raw wrapper: %w", errors.New(tc.raw))), "").Text()
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

func TestAttachedAdviceKeepsLocalAndDropsForeign(t *testing.T) {
	cause := Foreign(errors.New("raw cause"))
	err := errors.Join(
		Foreign(advisedError{errors.New("raw sibling"), []string{"raw server advice"}}),
		advisedError{cause, []string{"Retry: " + cause.Error(), "Confirm it with 'wendy device get-default'.", "Or connect by IP."}},
		advisedError{Foreign(errors.New("raw second cause")), []string{"Check its network connection and retry."}},
	)
	got := Explain(err, "")
	if len(got.Steps) != 3 {
		t.Errorf("local steps = %q", got.Steps)
	}
	if strings.Contains(got.Text(), "raw") {
		t.Errorf("foreign advice leaked: %q", got.Text())
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

}

func TestLocalMessagesRetainSpecifics(t *testing.T) {
	for _, tc := range []struct{ category, raw, want string }{
		{"", "no wendy.json found in /my/project", "No wendy.json found in /my/project."},
		{"config_invalid", "project config: service web: port must be between 1 and 65535", "Project config: service web: port must be between 1 and 65535."},
		{"cli_usage", "flag needs an argument: --device", "Flag needs an argument: --device."},
		{"tool_not_found", "install docker before building this app", "Install docker before building this app."},
		{"", "use a different device?", "Use a different device?"},
	} {
		got := Explain(errors.New(tc.raw), tc.category).Text()
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.category, got, tc.want)
		}
		assertSentences(t, got)
	}
	wrapped := fmt.Errorf("validating project: %w", errors.New("service web has no image"))
	if got := Explain(wrapped, "config_invalid").Message; got != "Validating project: service web has no image." {
		t.Errorf("lost local chain: %q", got)
	}
}

func TestPathErrorsNameTheirSubject(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{os.ErrNotExist, "Couldn't find /project/wendy.json. Check the path and try again."},
		{os.ErrPermission, "Access to /project/wendy.json was denied. Check its permissions and try again."},
		{errors.New("private filesystem detail"), "Couldn't access /project/wendy.json. Check the path and its permissions, then try again."},
	} {
		err := fmt.Errorf("loading project: %w", &os.PathError{Op: "open", Path: "/project/wendy.json", Err: tc.cause})
		got := Explain(err, "config_invalid").Message
		if got != "Loading project: "+strings.ToLower(tc.want[:1])+tc.want[1:] {
			t.Errorf("got %q, want %q", got, "Loading project: "+strings.ToLower(tc.want[:1])+tc.want[1:])
		}
		assertSentences(t, got)
	}
}

func TestForeignPreservesErrorContracts(t *testing.T) {
	cause := status.Error(codes.PermissionDenied, "private detail")
	err := Foreign(cause)
	if !errors.Is(err, cause) || err.Error() != cause.Error() || status.Code(err) != codes.PermissionDenied {
		t.Fatal("provenance marker changed error contract")
	}
	if Foreign(nil) != nil {
		t.Fatal("nil became an error")
	}
	if got := Server("unknown agent detail"); got != Generic {
		t.Errorf("unknown foreign message: %q", got)
	}
}

func TestCLIFlagErrorsRetainTheirSubject(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--device"}, "Parsing arguments: flag needs an argument: --device."},
		{[]string{"--no-such-flag"}, "Parsing arguments: unknown flag: --no-such-flag."},
		{[]string{"--count", "wrong"}, `Parsing arguments: invalid argument "wrong" for "--count" flag: the command or its arguments are not valid. Check the command and try again.`},
	} {
		flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
		flags.SetOutput(io.Discard)
		flags.String("device", "", "")
		flags.Int("count", 0, "")
		err := flags.Parse(tc.args)
		if err == nil {
			t.Fatal("invalid invocation succeeded")
		}
		got := Explain(fmt.Errorf("parsing arguments: %w", err), "cli_usage").Message
		if got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.args, got, tc.want)
		}
		assertSentences(t, got)
	}
}
