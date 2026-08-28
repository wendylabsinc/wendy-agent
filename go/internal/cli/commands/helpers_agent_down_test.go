package commands

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// refusedMTLSDial is the error the dial ladder records when nothing is bound to
// the mTLS port — the shape gRPC produces for ECONNREFUSED, tagged with the
// address the rung was dialled at.
func refusedMTLSDial(addr string) error {
	return mtlsAttemptError{
		addr: addr,
		err: status.Error(codes.Unavailable,
			`connection error: desc = "transport: Error while dialing: dial tcp `+addr+`: connect: connection refused"`),
	}
}

func TestIsConnectionRefusedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"refused mTLS dial", refusedMTLSDial("192.168.0.107:50052"), true},
		{"nil", nil, false},
		{
			name: "cert rejection",
			err: mtlsAttemptError{addr: "192.168.0.107:50052", err: status.Error(codes.Unavailable,
				`connection error: desc = "transport: authentication handshake failed: remote error: tls: bad certificate"`)},
		},
		{
			name: "handshake timeout",
			err: mtlsAttemptError{addr: "192.168.0.107:50052", err: status.Error(codes.Unavailable,
				`connection error: desc = "transport: Error while dialing: dial tcp 192.168.0.107:50052: i/o timeout"`)},
		},
		{"unrelated", errors.New("loading TLS cert: bad key"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConnectionRefusedError(tc.err); got != tc.want {
				t.Errorf("isConnectionRefusedError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A refused mTLS port means the agent is not running — most often because
// `wendy device update` just restarted it. Reporting that as "Unauthorized. Run
// 'wendy auth login'" blamed credentials that were never examined, since the
// connection died before any certificate was presented.
func TestProvisionedAgentConnectError_RefusedPortIsNotAnAuthFailure(t *testing.T) {
	err := provisionedAgentConnectError(refusedMTLSDial("192.168.0.107:50052"))

	if errors.Is(err, errProvisionedAgentUnauthorized) {
		t.Fatalf("a refused mTLS port must not be reported as unauthorized, got %v", err)
	}
	msg := err.Error()
	for _, forbidden := range []string{"Unauthorized", "wendy auth login", "refresh-certs"} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("message must not suggest %q, got %q", forbidden, msg)
		}
	}
	if !strings.Contains(msg, "192.168.0.107:50052") {
		t.Errorf("message should name the port that refused, got %q", msg)
	}
	if !strings.Contains(msg, "device update") {
		t.Errorf("message should point at the restart window as the likely cause, got %q", msg)
	}
	var notListening agentNotListeningError
	if !errors.As(err, &notListening) {
		t.Fatalf("want agentNotListeningError, got %T", err)
	}
	if notListening.Unwrap() == nil {
		t.Error("the underlying mTLS error should stay reachable through Unwrap")
	}
}

// formatError in cmd/wendy rewrites everything from "rpc error: code = "
// onwards into "Could not connect to device. Is it powered on and connected to
// the network?" — which contradicts this diagnosis, because the host answered
// and only the agent is missing. Keeping the marker out of the message is what
// makes formatError pass it through untouched.
func TestAgentNotListeningError_CarriesNoGRPCMarkerForFormatError(t *testing.T) {
	msg := provisionedAgentConnectError(refusedMTLSDial("192.168.0.107:50052")).Error()
	if strings.Contains(msg, "rpc error: code = ") {
		t.Fatalf("message would be rewritten by cmd/wendy formatError, got %q", msg)
	}
}

// Everything that is not a refused connection keeps the original unauthorized
// diagnosis — including a nil cause, which is the "no certificates loaded at
// all" case and genuinely does mean the user must log in.
func TestProvisionedAgentConnectError_NonRefusedStaysUnauthorized(t *testing.T) {
	tests := []struct {
		name  string
		cause error
	}{
		{"no certs loaded", nil},
		{
			name: "cert rejected",
			cause: mtlsAttemptError{addr: "192.168.0.107:50052", err: status.Error(codes.Unavailable,
				`connection error: desc = "transport: authentication handshake failed: remote error: tls: bad certificate"`)},
		},
		{
			name: "handshake timeout",
			cause: mtlsAttemptError{addr: "192.168.0.107:50052", err: status.Error(codes.Unavailable,
				`connection error: desc = "transport: Error while dialing: dial tcp 192.168.0.107:50052: i/o timeout"`)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := provisionedAgentConnectError(tc.cause)
			if !errors.Is(err, errProvisionedAgentUnauthorized) {
				t.Fatalf("provisionedAgentConnectError(%v) = %v, want unauthorized", tc.cause, err)
			}
		})
	}
}

// The ladder's address tagging is what lets the not-listening message name a
// port, so it has to keep rendering exactly like the "<addr>: <err>" wrapping
// it replaced — several diagnostics quote that string verbatim.
func TestMTLSAttemptError_RendersAddressPrefixedCause(t *testing.T) {
	cause := errors.New("connect: connection refused")
	err := mtlsAttemptError{addr: "192.168.0.107:50052", err: cause}

	if got, want := err.Error(), "192.168.0.107:50052: connect: connection refused"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("the wrapped cause should stay matchable with errors.Is")
	}
}
