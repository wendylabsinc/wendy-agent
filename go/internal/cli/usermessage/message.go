// Package usermessage is the boundary between failures and CLI display text.
// CLI-authored messages are sentences; foreign failures select approved prose.
package usermessage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"unicode"

	"github.com/spf13/pflag"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const Generic = "Something went wrong. Try again."
const Offline = "Device is offline. Make sure it's on and connected, then try again."
const NotFound = "This device no longer exists or you don't have access to it."

// Report keeps recovery steps separate for the existing JSON error envelope.
type Report struct {
	Message     string
	Steps       []string
	mappedStart bool
}

func (r Report) Text() string {
	text := r.Message
	for _, step := range r.Steps {
		text += "\n  " + step
	}
	return text
}

func report(message string, steps ...string) Report { return Report{Message: message, Steps: steps} }

// Explain replaces foreign failures inside CLI-authored message chains. Recovery
// steps remain separate from the message, for both terminal and JSON output.
func Explain(err error, category string) Report {
	return explain(err, category, err)
}

func explain(err error, category string, scope error) Report {
	if err == nil {
		return Report{}
	}
	result := explainNode(err, category, scope)
	result.Message = sentence(result.Message)
	return result
}

func explainNode(err error, category string, scope error) Report {
	if err == nil {
		return Report{}
	}
	if err == config.ErrNotLoggedIn {
		return Report{Message: "Sign in again to continue.", Steps: []string{"Run 'wendy auth login', then try again."}, mappedStart: true}
	}
	if foreignNode(err) {
		result := mapForeign(err, category, scope)
		result.mappedStart = true
		return result
	}
	text := err.Error()
	var ownSteps []string
	if carrier, ok := err.(interface{ NextSteps() []string }); ok {
		for _, step := range carrier.NextSteps() {
			if !containsForeignText(step, scope) {
				ownSteps = appendUnique(ownSteps, step)
			}
		}
		if custom, ok := err.(interface{ MessageWithoutSteps() string }); ok {
			text = custom.MessageWithoutSteps()
		}
		for _, step := range carrier.NextSteps() {
			text = withoutStep(text, step)
		}
	}
	result := report(text, ownSteps...)
	var replacements []messageReplacement
	for _, cause := range causes(err) {
		raw := cause.Error()
		if raw == "" || !strings.Contains(text, raw) {
			continue
		}
		child := explainNode(cause, category, scope)
		replacements = append(replacements, messageReplacement{raw, child.Message, child.mappedStart})
		if strings.HasPrefix(text, raw) && child.mappedStart {
			result.mappedStart = true
		}
		if len(ownSteps) == 0 {
			for _, step := range child.Steps {
				result.Steps = appendUnique(result.Steps, step)
			}
		}
	}
	result.Message = replaceMessages(text, replacements)
	return result
}

type messageReplacement struct {
	raw, message string
	mappedStart  bool
}

// Match the original text once, choosing the longest match at each position.
// Repeated or overlapping causes cannot leave raw fragments behind, and mapped
// prose is never scanned again as if it were another foreign input.
func replaceMessages(text string, replacements []messageReplacement) string {
	var out strings.Builder
	capitalizeNext := false
	for text != "" {
		position, selected := -1, -1
		for i, replacement := range replacements {
			at := strings.Index(text, replacement.raw)
			if at >= 0 && (position < 0 || at < position || at == position && len(replacement.raw) > len(replacements[selected].raw)) {
				position, selected = at, i
			}
		}
		if position < 0 {
			if capitalizeNext {
				text = capitalize(text)
			}
			out.WriteString(text)
			break
		}
		context := text[:position]
		if capitalizeNext {
			context = capitalize(context)
		}
		out.WriteString(context)
		replacement := replacements[selected]
		message := replacement.message
		if replacement.mappedStart && strings.HasSuffix(strings.TrimRight(out.String(), " \t"), ":") {
			message = sentenceContinuation(message)
		}
		out.WriteString(message)
		text = text[position+len(replacement.raw):]
		capitalizeNext = false
		if strings.HasSuffix(replacement.message, ".") && strings.HasPrefix(text, "; ") {
			out.WriteString(" ")
			text = text[2:]
			capitalizeNext = true
		}
	}
	return out.String()
}

// Only mapped prose is adjusted here; authored names and executable tokens stay
// untouched. Wendy and Wi-Fi are the proper names that begin mapped messages.
func sentenceContinuation(message string) string {
	if strings.HasPrefix(message, "Wendy ") || strings.HasPrefix(message, "Wi-Fi ") {
		return message
	}
	runes := []rune(message)
	if len(runes) > 0 {
		runes[0] = unicode.ToLower(runes[0])
	}
	return string(runes)
}

func causes(err error) []error {
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		return e.Unwrap()
	case interface{ Unwrap() error }:
		if e.Unwrap() != nil {
			return []error{e.Unwrap()}
		}
	}
	return nil
}

func appendUnique(steps []string, step string) []string {
	if strings.TrimSpace(step) == "" {
		return steps
	}
	for _, existing := range steps {
		if existing == step {
			return steps
		}
	}
	return append(steps, step)
}

func withoutStep(text, step string) string {
	for _, sep := range []string{"\n  ", "\n", " "} {
		if strings.Contains(text, sep+step) {
			return strings.Replace(text, sep+step, "", 1)
		}
	}
	return text
}

func containsForeignText(text string, err error) bool {
	if err == nil {
		return false
	}
	if foreignNode(err) {
		if raw := err.Error(); raw != "" && strings.Contains(text, raw) {
			return true
		}
		if st, ok := status.FromError(err); ok && st.Message() != "" && strings.Contains(text, st.Message()) {
			return true
		}
		return false
	}
	for _, cause := range causes(err) {
		if containsForeignText(text, cause) {
			return true
		}
	}
	return false
}

func mapForeign(err error, category string, scope error) Report {
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
		if strings.Contains(lower, "asset not found") {
			return report(NotFound)
		}
		return report("The requested item was not found.")
	}
	var path *os.PathError
	if errors.As(err, &path) {
		switch {
		case errors.Is(path, os.ErrNotExist):
			return report("Couldn't find " + path.Path + ". Check the path and try again.")
		case errors.Is(path, os.ErrPermission):
			return report("Access to " + path.Path + " was denied. Check its permissions and try again.")
		default:
			return report("Couldn't access " + path.Path + ". Check the path and its permissions, then try again.")
		}
	}
	var invalidFlag *pflag.InvalidValueError
	if errors.As(err, &invalidFlag) {
		return report("The value for --" + invalidFlag.GetFlag().Name + " is not valid. Check the value and try again.")
	}
	// Known refusal reasons select fixed client-authored guidance too. They are
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
	scopeText := strings.ToLower(strings.ReplaceAll(scope.Error(), err.Error(), ""))
	cloud := strings.Contains(scopeText, "registering deployment with cloud") || strings.Contains(scopeText, "issuing certificate") || strings.Contains(scopeText, "refreshing certificate") || strings.Contains(scopeText, "creating enrollment token") || strings.Contains(scopeText, "connecting to cloud")
	pki := strings.Contains(scopeText, "pki-core")
	cert := category == "device_tls_rejected" || strings.Contains(lower, "tls:") || strings.Contains(lower, "bad certificate") || strings.Contains(lower, "certificate required") || strings.Contains(lower, "x509:")
	var https *url.Error
	if cert && code == codes.Unavailable && !errors.As(scope, &https) && !cloud && !pki {
		return report("The device rejected the secure connection. Its clock or certificates may need attention.",
			"If the device clock may be wrong, run 'wendy device sync-time' on the same LAN, then retry.",
			"For full connection details, rerun with WENDY_TLS_DEBUG=1.")
	}
	if cert {
		return report("The secure connection could not be verified. Check your network and certificate settings, then try again.")
	}
	device := "device"
	var dial interface{ DeviceAddress() string }
	if errors.As(scope, &dial) && safeAddress(dial.DeviceAddress()) {
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
	if code == codes.Unavailable && !errors.As(scope, &https) && strings.Contains(lower, "authentication handshake failed") && !cloud && !pki {
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

// Text is used for inline errors. Mark untyped foreign errors with Foreign at
// their source; use Server for a response received as a string.
func Text(err error) string {
	return Explain(err, "").Text()
}

// Server converts an untyped server failure into approved text. Prefer keeping
// typed errors intact until Explain so status codes remain available.
func Server(raw string) string { return Text(Foreign(errors.New(raw))) }

type foreignError struct{ error }

func (e *foreignError) Unwrap() error { return e.error }

// Foreign retains provenance when an agent, HTTP body or library supplies an
// untyped error. It preserves Error, Is, As and status information for callers.
func Foreign(err error) error {
	if err == nil {
		return nil
	}
	return &foreignError{err}
}

// foreignNode classifies this node, rather than inheriting its child's status:
// a local fmt wrapper is context to retain, even when its cause is foreign.
func foreignNode(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(*foreignError); ok {
		return true
	}
	if _, ok := err.(net.Error); ok {
		return true
	}
	if _, ok := err.(interface{ GRPCStatus() *status.Status }); ok {
		return true
	}
	typ := reflect.TypeOf(err)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	pkg := typ.PkgPath()
	if pkg != "fmt" && pkg != "errors" && pkg != "github.com/spf13/pflag" && !strings.HasPrefix(pkg, "github.com/wendylabsinc/wendy/go/internal/cli/") && !strings.HasPrefix(pkg, "github.com/wendylabsinc/wendy/go/internal/shared/") && pkg != "github.com/wendylabsinc/wendy/go/cmd/wendy" {
		return true
	}
	if len(causes(err)) > 0 {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, marker := range []string{"rpc error: code =", "cloud tunnel closed by broker:", "tls:", "x509:", "authentication handshake failed"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, sentinel := range []error{io.EOF, io.ErrUnexpectedEOF, io.ErrClosedPipe, io.ErrNoProgress, io.ErrShortBuffer, io.ErrShortWrite, exec.ErrNotFound, os.ErrPermission, os.ErrNotExist, os.ErrExist, os.ErrInvalid, os.ErrClosed, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

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
	var steps []string
	var replacements []messageReplacement
	for i, arg := range safe {
		if err, ok := arg.(error); ok {
			scope := fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
			r := explain(err, "", scope)
			safe[i] = r.Message
			if r.mappedStart && r.Message != "" {
				replacements = append(replacements, messageReplacement{r.Message, r.Message, true})
			}
			for _, step := range r.Steps {
				steps = appendUnique(steps, step)
			}
		}
	}
	message := sentence(replaceMessages(fmt.Sprintf(format, safe...), replacements))
	if len(steps) == 0 {
		return message
	}
	// Punctuate the message, never a command carried as a recovery step.
	end := len(strings.TrimRightFunc(message, unicode.IsSpace))
	return (Report{Message: message[:end], Steps: steps}).Text() + message[end:]
}

func Printf(format string, args ...any) (int, error) { return Fprintf(os.Stdout, format, args...) }
func Fprintf(w io.Writer, format string, args ...any) (int, error) {
	return fmt.Fprint(w, Sprintf(format, args...))
}

func capitalize(s string) string {
	// Capitalize only an ordinary leading word, never a flag, path, quoted
	// name, terminal control sequence, or a label following a status glyph.
	start := len(s) - len(strings.TrimLeftFunc(s, unicode.IsSpace))
	word := strings.Fields(s[start:])
	if len(word) == 0 {
		return s
	}
	token := strings.TrimRight(word[0], ":;,.?!")
	for _, r := range token {
		if !unicode.IsLetter(r) && r != '\'' {
			return s
		}
	}
	r := []rune(s[start:])
	if len(r) == 0 || !unicode.IsLetter(r[0]) {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return s[:start] + string(r)
}
func sentence(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	s = capitalize(s)
	end := len(strings.TrimRightFunc(s, unicode.IsSpace))
	if !strings.ContainsAny(s[end-1:end], ".?!") {
		s = s[:end] + "." + s[end:]
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
