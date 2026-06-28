package diag

import (
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Severity buckets an error for the crash-reporter trigger.
type Severity string

const (
	Recoverable   Severity = "recoverable"
	Unrecoverable Severity = "unrecoverable"
)

// buildFailure marks an error as an unrecoverable build failure.
type buildFailure struct{ err error }

func (b buildFailure) Error() string { return b.err.Error() }
func (b buildFailure) Unwrap() error { return b.err }

// MarkBuildFailure tags err so Classify treats it as Unrecoverable.
func MarkBuildFailure(err error) error {
	if err == nil {
		return nil
	}
	return buildFailure{err: err}
}

// DiagError attaches structured context to an error without changing how it
// renders to the user (Error() is just the wrapped chain).
type DiagError struct {
	err    error
	op     string
	device string
	stage  string
}

// Wrap attaches an operation label to err.
func Wrap(err error, op string) *DiagError { return &DiagError{err: err, op: op} }

func (e *DiagError) WithDevice(name string) *DiagError { e.device = name; return e }
func (e *DiagError) WithStage(stage string) *DiagError { e.stage = stage; return e }

func (e *DiagError) Error() string {
	if e.op != "" {
		return e.op + ": " + e.err.Error()
	}
	return e.err.Error()
}

func (e *DiagError) Unwrap() error { return e.err }

// Fields returns the structured context for inclusion in a report.
func (e *DiagError) Fields() map[string]string {
	m := map[string]string{}
	if e.op != "" {
		m["op"] = e.op
	}
	if e.device != "" {
		m["device"] = e.device
	}
	if e.stage != "" {
		m["stage"] = e.stage
	}
	return m
}

// Classify buckets err. Build-failure markers and gRPC Internal/Unknown/DataLoss
// are unrecoverable; everything else (user errors, Unavailable, plain errors) is
// recoverable.
func Classify(err error) Severity {
	if err == nil {
		return Recoverable
	}
	var bf buildFailure
	if errors.As(err, &bf) {
		return Unrecoverable
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		switch st.Code() {
		case codes.Internal, codes.Unknown, codes.DataLoss:
			return Unrecoverable
		}
	}
	return Recoverable
}

// Chain renders the full unwrapped error chain (pre-redaction).
func Chain(err error) string {
	var parts []string
	for err != nil {
		parts = append(parts, err.Error())
		err = errors.Unwrap(err)
	}
	return strings.Join(parts, "\n  ↳ ")
}
