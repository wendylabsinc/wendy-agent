// Package usermessage is the boundary between failures and CLI display text.
// Error strings may select a known reason, but are never returned to a person.
package usermessage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"unicode"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const Generic = "Something went wrong. Try again."
const Offline = "Device is offline. Make sure it's on and connected, then try again."
const NotFound = "This device no longer exists or you don't have access to it."

// Report keeps recovery steps separate for the existing JSON error envelope.
type Report struct {
	Message string
	Steps   []string
}

func (r Report) Text() string {
	text := r.Message
	for _, step := range r.Steps {
		text += "\n  " + step
	}
	return text
}

func report(message string, steps ...string) Report { return Report{message, steps} }

// Explain maps transport status and the CLI's bounded failure category to our
// sentences. Neither wrapped context nor server descriptions are display text.
// Category comes from ExecutionErrorClass; it does not affect exit/retry logic.
func Explain(err error, category string) (result Report) {
	defer func() { result.Steps = approvedSteps(err, result.Steps) }()
	if err == nil {
		return Report{}
	}
	if errors.Is(err, config.ErrNotLoggedIn) {
		return report("Sign in again to continue.", "Run 'wendy auth login', then try again.")
	}
	if category == "device_identity_mismatch" {
		return report("Connection blocked because the device's identity has changed.",
			"If this change was unexpected, keep the saved identity and verify the device first.",
			"If you intentionally reset or unenrolled the device, run 'wendy device unpin <host>' and retry.")
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	code := status.Code(err)
	// Broker verdicts live inside a connection failure. Other typed statuses
	// keep their code even if a description happens to contain another status.
	serialized := ""
	if _, verdict, ok := strings.Cut(msg, "cloud tunnel closed by broker: "); ok {
		serialized = verdict
	} else if _, typed := status.FromError(err); !typed {
		serialized = msg
	}
	if i := strings.Index(serialized, "rpc error: code = "); i >= 0 {
		rest := serialized[i+len("rpc error: code = "):]
		for c := codes.Canceled; c <= codes.Unauthenticated; c++ {
			if strings.HasPrefix(rest, c.String()+" desc = ") {
				code = c
				break
			}
		}
	}
	// A refusal must keep its reason even when an unrelated description also
	// mentions a missing or offline device.
	switch code {
	case codes.Unauthenticated:
		return report("Sign in again to continue.", "Run 'wendy auth login', then try again.")
	case codes.PermissionDenied:
		return report("You don't have permission to do this. Check your access and try again.")
	case codes.NotFound:
		return report(NotFound)
	}
	// Known local refusals select fixed client-authored guidance too. They are
	// never copied from the input, and cannot carry additional server details.
	for _, known := range []struct{ reason, message string }{
		{"enter a unicast ipv4 address for the robot", "Enter a unicast IPv4 address for the robot."},
		{"create the simulator before setting it as default", "Create the simulator before setting it as default."},
		{"network camera management is not supported by wendy agent for macos", "Network camera management is not supported by Wendy Agent for Mac."},
		{"this device's wendy agent cannot scan its network for sensor sources", "This device cannot scan its network for sensor sources. Update its agent and try again."},
		{"not authorized to pair with this device", "You don't have permission to pair with this device. Check your access and try again."},
		{"device is not reachable", "Device is not reachable. Make sure it's on and connected, then try again."},
		{"the device did not answer in time", "The device did not answer in time. Check its connection and try again."},
		{"the device did not finish scanning its network in time", "The device did not finish scanning its network in time. Try again."},
	} {
		if strings.Contains(lower, known.reason) {
			return report(known.message)
		}
	}
	if strings.Contains(lower, "device is offline") || strings.Contains(lower, "asset not online") || strings.Contains(lower, "device offline") || category == "device_offline" {
		return report(Offline)
	}
	if strings.Contains(lower, "asset not found") {
		return report(NotFound)
	}
	switch category {
	case "memory_ambiguous":
		return report("More than one saved note matches. Use a full note ID and try again.")

	case "auth_required", "device_auth_required":
		return report("Sign in again to continue.", "Run 'wendy auth login', then try again.")
	case "auth_session_ambiguous":
		return report("Choose a sign-in session and try again.", "Run 'wendy auth default' to choose a session.")
	case "cli_usage":
		return report("The command or its arguments are not valid. Check the command and try again.")
	case "config_invalid":
		return report("The project settings are not valid. Check your settings and try again.")
	case "no_device":
		return report("No matching device was found. Check the device and try again.")
	case "device_ambiguous":
		return report("More than one device matches. Choose a device and try again.")
	case "device_org_mismatch":
		return report("This device belongs to a different organization. Choose the matching organization and try again.")
	case "project_target_mismatch":
		return report("This project cannot run on the selected device. Choose a compatible device and try again.")
	case "build_failed":
		return report("The app could not be built. Check the build output and try again.")
	case "builder_unavailable":
		return report("The build service is unavailable. Try again later.")
	case "transfer_failed":
		return report("The app could not be sent to the device. Check the connection and try again.")
	case "container_start_failed":
		return report("The app could not start. Check the app's logs and try again.")
	case "readiness_timeout":
		return report("The app did not become ready in time. Check the app's logs and try again.")
	case "app_crashed":
		return report("The app stopped unexpectedly. Check the app's logs and try again.")
	case "registry_auth":
		return report("Sign in to the image registry and try again.")
	case "registry_unavailable":
		return report("The image registry is unavailable. Try again later.")
	case "tool_not_found":
		return report("A required tool is missing. Install it and try again.")
	case "disk_full":
		return report("There is not enough storage space. Free some space and try again.")
	}
	cloud := strings.Contains(lower, "issuing certificate") || strings.Contains(lower, "refreshing certificate") || strings.Contains(lower, "creating enrollment token") || strings.Contains(lower, "connecting to cloud")
	pki := strings.Contains(lower, "pki-core")
	cert := category == "device_tls_rejected" || strings.Contains(lower, "tls:") || strings.Contains(lower, "bad certificate") || strings.Contains(lower, "certificate required") || strings.Contains(lower, "x509:")
	if cert && !cloud && !pki {
		return report("The device rejected the secure connection. Its clock or certificates may need attention.",
			"If the device clock may be wrong, run 'wendy device sync-time' on the same LAN, then retry.",
			"For full connection details, rerun with WENDY_TLS_DEBUG=1.")
	}
	device := "device"
	var dial interface{ DeviceAddress() string }
	if errors.As(err, &dial) && safeAddress(dial.DeviceAddress()) {
		device += " at " + dial.DeviceAddress()
	}
	if (code == codes.Unavailable || category == "device_not_resolved") && (strings.Contains(lower, "produced zero addresses") || strings.Contains(lower, "no such host")) {
		host := strings.TrimPrefix(device, "device at ")
		if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
			host = h
		}
		if host != "device" {
			hint := "Check the name, or connect by IP address."
			if strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
				hint = "The device may be offline, or local discovery may be blocked on this network. Connect by IP address to check."
			}
			return report("Could not resolve device host "+strings.TrimSuffix(host, ".")+".", hint)
		}
	}
	if code == codes.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
		if device != "device" {
			return report("Connection to " + device + " timed out.")
		}
		return report("Connection timed out.")
	}
	if strings.Contains(lower, "authentication handshake failed") && !cloud && !pki {
		return report("The secure connection dropped. The device or its Wendy Cloud tunnel may be offline.", "For full connection details, rerun with WENDY_TLS_DEBUG=1.")
	}
	switch code {
	case codes.Unimplemented:
		if strings.Contains(msg, "Wi-Fi configuration is currently not supported by Wendy Agent for Mac.") {
			return report("Wi-Fi configuration is not supported by Wendy Agent for Mac.")
		}
		if strings.Contains(msg, "Container volume management is currently not supported by Wendy Agent for Mac.") {
			return report("Container volume management is not supported by Wendy Agent for Mac.")
		}
		if strings.Contains(msg, "Audio device management is currently not supported by Wendy Agent for Mac.") {
			return report("Audio device management is not supported by Wendy Agent for Mac.")
		}
		return report("Not supported by this agent version. Try updating the agent.")
	case codes.Unavailable:
		if pki {
			return report("The local certificate service is unavailable. Check that it is running and try again.")
		}
		if cloud {
			return report("Wendy Cloud is unavailable. Please try again later.")
		}
		if strings.Contains(lower, "connection refused") {
			return report("Could not connect to " + device + ". Make sure it's on and connected, then try again.")
		}
		return report(capitalize(device) + " is unavailable.")
	case codes.InvalidArgument, codes.OutOfRange:
		return report("The requested settings are not valid. Check them and try again.")
	case codes.AlreadyExists:
		return report("This already exists. Choose a different name and try again.")
	case codes.ResourceExhausted:
		return report("The device or service has reached its limit. Free resources and try again.")
	case codes.FailedPrecondition:
		if strings.Contains(lower, "update is already in progress") {
			return report("Another update is already in progress. Wait for it to finish, then retry.", "If no update is running and this keeps happening, restart the device and try again.")
		}
		return report("The device is not ready for this action. Check its state and try again.")
	case codes.Aborted:
		return report("The action was interrupted. Try again.")
	case codes.Canceled:
		return report("The action was cancelled.")
	}
	var network net.Error
	switch {
	case errors.Is(err, os.ErrPermission):
		return report("Access was denied. Check your permissions and try again.")
	case errors.Is(err, os.ErrNotExist):
		return report("A required file could not be found. Check the path and try again.")
	case errors.As(err, &network):
		return report("Could not connect. Check your network connection and try again.")
	case errors.Is(err, context.Canceled):
		return report("The action was cancelled.")
	}
	return report(Generic)
}

// Text is used for inline messages, including failures received as strings.
func Text(err error) string {
	Debug(err)
	return Explain(err, "").Text()
}

// Server converts an untyped server failure into approved text. Prefer keeping
// typed errors intact until Explain so status codes remain available.
func Server(raw string) string { return Text(errors.New(raw)) }

// Debug keeps raw diagnostics behind the existing explicit debug switch.
func Debug(err error) {
	if err != nil && os.Getenv("WENDY_TLS_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[tls-debug] %v\n", err)
	}
}

// Sprintf formats authored inline context, mapping every error argument first.
// Successful payloads and application log streams must not pass through this.
func Sprintf(format string, args ...any) string {
	safe := append([]any(nil), args...)
	for i, arg := range safe {
		if err, ok := arg.(error); ok {
			safe[i] = Text(err)
		}
	}
	lines := strings.Split(fmt.Sprintf(format, safe...), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = sentence(line)
		}
	}
	return strings.Join(lines, "\n")
}

func Printf(format string, args ...any) (int, error) { return Fprintf(os.Stdout, format, args...) }
func Fprintf(w io.Writer, format string, args ...any) (int, error) {
	return fmt.Fprint(w, Sprintf(format, args...))
}

func capitalize(s string) string {
	r := []rune(s)
	// Styled names already carry the caller's chosen presentation. Do not
	// rewrite bytes inside terminal control sequences.
	if strings.ContainsRune(s, '\x1b') {
		return s
	}
	for i, c := range r {
		if unicode.IsLetter(c) {
			r[i] = unicode.ToUpper(c)
			break
		}
	}
	return string(r)
}
func sentence(s string) string {
	s = strings.TrimSpace(s)
	s = capitalize(s)
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, "!") {
		s += "."
	}
	return s
}
func safeAddress(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:[]%_", c)) {
			return false
		}
	}
	return true
}

// Only explicitly approved recovery sentences may cross from an error's
// attached advice into human output. The map values, never the input, are used.
func approvedSteps(err error, steps []string) []string {
	approved := []string{
		"Confirm it with 'wendy device get-default'.",
		"Confirm it with 'wendy device get-default'; change it with 'wendy device set-default' or clear it with 'wendy device unset-default'.",
		"Check its network connection and retry.",
		"For connection details rerun with WENDY_TLS_DEBUG=1.",
	}
	var visit func(error)
	visit = func(err error) {
		if err == nil {
			return
		}
		if carrier, ok := err.(interface{ NextSteps() []string }); ok {
			for _, input := range carrier.NextSteps() {
				for _, sentence := range approved {
					if input == sentence {
						found := false
						for _, step := range steps {
							if step == sentence {
								found = true
							}
						}
						if !found {
							steps = append(steps, sentence)
						}
					}
				}
			}
		}
		switch inner := err.(type) {
		case interface{ Unwrap() []error }:
			for _, e := range inner.Unwrap() {
				visit(e)
			}
		case interface{ Unwrap() error }:
			visit(inner.Unwrap())
		}
	}
	visit(err)
	return steps
}
