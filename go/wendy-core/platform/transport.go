package platform

import "context"

// MetadataEntry is one RPC metadata value. Name is a lowercase gRPC metadata
// name. Value contains ASCII bytes, or raw (not base64) bytes for a -bin name.
type MetadataEntry struct {
	// Name is the lowercase metadata name, including any -bin suffix.
	Name string
	// Value is the unencoded metadata value.
	Value []byte
}

// Metadata preserves repeated names, entry order and binary values. Hosts
// validate gRPC names and values rather than silently discarding invalid entries.
type Metadata []MetadataEntry

// RPCRequest is a serialized protobuf RPC. Its data may contain credentials.
type RPCRequest struct {
	// Method is the fully qualified /package.Service/Method path.
	Method string
	// Message is the serialized request protobuf, including a valid empty message.
	Message []byte
	// Metadata is additional metadata, before the host adds authentication.
	Metadata Metadata
}

// RPCResponse contains a unary response and its initial and trailing metadata.
// On an RPC status failure, the error also retains both sets of metadata.
type RPCResponse struct {
	// Message is the serialized response protobuf; it may be empty.
	Message []byte
	// InitialMetadata contains the response headers.
	InitialMetadata Metadata
	// TrailingMetadata contains the response trailers.
	TrailingMetadata Metadata
}

// Transport sends serialized protobuf RPCs using host-owned connections.
// The host owns TLS and authentication. Core neither opens connections nor
// encrypts data. Implementations must be safe for concurrent use, honor context
// cancellation/deadlines and avoid automatic retries of application requests.
// They must not log payloads or put credential bytes in errors.
//
// Requests are borrowed and immutable until the opening call returns. Response
// messages and metadata belong to the caller and must not be reused by the host.
// A non-OK remote status is an *RPCStatusError, accessible with errors.As even
// when wrapped. Local errors may retain their native types; cancellation and
// deadline errors must match context.Canceled/context.DeadlineExceeded through
// errors.Is, including when represented as RPCStatusError.
type Transport interface {
	// Unary sends one request. ctx carries the call deadline; without one the
	// host may apply its default. Available metadata is retained on failure.
	Unary(ctx context.Context, request RPCRequest) (RPCResponse, error)

	// ServerStream sends one request and starts a server-streaming RPC. ctx
	// controls the entire stream lifetime, including blocked Header/Recv calls.
	// Opening failures preserve available metadata in RPCStatusError.
	// The caller must close a successfully opened stream, even after EOF/error.
	ServerStream(ctx context.Context, request RPCRequest) (ServerStream, error)
}

// ServerStream receives one serialized protobuf message per Recv call. Only one
// Recv may run at a time. Close may run concurrently with a blocked Header/Recv;
// other methods need not support concurrent calls.
type ServerStream interface {
	// Header waits for initial metadata, or returns an error if unavailable.
	// Repeated calls return the same metadata/error, owned by the caller.
	Header() (Metadata, error)

	// Recv returns the next message (which may be empty). io.EOF means successful
	// completion; a non-OK status returns *RPCStatusError with metadata. After a
	// terminal result, subsequent calls return that result without more messages.
	Recv() ([]byte, error)

	// Trailer returns the trailing metadata after Recv has returned EOF/error.
	// Before completion it returns nil. Returned data belongs to the caller.
	Trailer() Metadata

	// Close releases resources and cancels an unfinished RPC, unblocking any
	// Header/Recv with an error matching context.Canceled. It is idempotent.
	Close() error
}
