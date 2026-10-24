package platform

import (
	"context"
	"strconv"
)

// RPCStatusCode is a numeric gRPC wire status, independent of grpc-go. Unknown
// values are preserved when crossing host boundaries.
type RPCStatusCode uint32

const (
	// RPCOK indicates successful completion; it must not be returned as an error.
	RPCOK RPCStatusCode = iota
	// RPCCanceled indicates cancellation.
	RPCCanceled
	// RPCUnknown indicates an unclassified failure.
	RPCUnknown
	// RPCInvalidArgument indicates an invalid argument.
	RPCInvalidArgument
	// RPCDeadlineExceeded indicates an expired deadline.
	RPCDeadlineExceeded
	// RPCNotFound indicates a missing resource.
	RPCNotFound
	// RPCAlreadyExists indicates an existing resource.
	RPCAlreadyExists
	// RPCPermissionDenied indicates insufficient permission.
	RPCPermissionDenied
	// RPCResourceExhausted indicates an exhausted resource limit.
	RPCResourceExhausted
	// RPCFailedPrecondition indicates an unsuitable current state.
	RPCFailedPrecondition
	// RPCAborted indicates an aborted operation.
	RPCAborted
	// RPCOutOfRange indicates an argument outside its supported range.
	RPCOutOfRange
	// RPCUnimplemented indicates an unsupported operation.
	RPCUnimplemented
	// RPCInternal indicates an internal service error.
	RPCInternal
	// RPCUnavailable indicates a temporarily unavailable service.
	RPCUnavailable
	// RPCDataLoss indicates lost or corrupted data.
	RPCDataLoss
	// RPCUnauthenticated indicates missing or invalid authentication.
	RPCUnauthenticated
)

// RPCStatusError preserves a non-OK gRPC status and response metadata. Message,
// metadata and Cause may contain sensitive server diagnostics: callers must
// redact them before display/logging. Error deliberately prints only the code.
// Hosts must never include credential-bearing payloads in errors or causes.
type RPCStatusError struct {
	// Code is the non-OK gRPC wire status reported by the host.
	Code RPCStatusCode
	// Message is the original remote status description, without display formatting.
	Message string
	// InitialMetadata contains any response headers received before failure.
	InitialMetadata Metadata
	// TrailingMetadata contains any response trailers, including binary status details.
	TrailingMetadata Metadata
	// Cause retains the original transport error without displaying it.
	Cause error
}

// Error returns a diagnostic that excludes remote messages, metadata and causes.
func (e *RPCStatusError) Error() string {
	return "RPC failed with status " + strconv.FormatUint(uint64(e.Code), 10)
}

// Is matches cancellation and deadline statuses to the corresponding context
// errors even when the host's original error uses a different representation.
func (e *RPCStatusError) Is(target error) bool {
	return (e.Code == RPCCanceled && target == context.Canceled) ||
		(e.Code == RPCDeadlineExceeded && target == context.DeadlineExceeded)
}

// Unwrap retains the host error for errors.Is/errors.As without displaying it.
func (e *RPCStatusError) Unwrap() error { return e.Cause }
