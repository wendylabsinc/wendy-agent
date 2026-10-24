// Package platformtest provides function-backed host fakes for Core tests.
// Set each function a test expects to call; an unset function panics so unexpected
// host calls fail immediately. Callbacks own behavior, including cancellation,
// stream lifecycle and synchronization when a fake is used concurrently.
package platformtest

import (
	"context"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
)

// Signer delegates signing and public identity access to test functions.
type Signer struct {
	// AlgorithmFunc reports the fake's signature scheme.
	AlgorithmFunc func() platform.SignatureAlgorithm
	// CertificateChainFunc supplies the fake's public certificate chain.
	CertificateChainFunc func(context.Context) ([][]byte, error)
	// PublicKeyFunc supplies the fake's public key.
	PublicKeyFunc func(context.Context) ([]byte, error)
	// SignFunc handles exact signing input and returns a scripted signature/error.
	SignFunc func(context.Context, []byte) ([]byte, error)
}

// Algorithm implements platform.Signer.
func (s Signer) Algorithm() platform.SignatureAlgorithm { return s.AlgorithmFunc() }

// CertificateChain implements platform.Signer.
func (s Signer) CertificateChain(ctx context.Context) ([][]byte, error) {
	return s.CertificateChainFunc(ctx)
}

// PublicKey implements platform.Signer.
func (s Signer) PublicKey(ctx context.Context) ([]byte, error) { return s.PublicKeyFunc(ctx) }

// Sign implements platform.Signer.
func (s Signer) Sign(ctx context.Context, message []byte) ([]byte, error) {
	return s.SignFunc(ctx, message)
}

// Transport delegates unary and server-streaming RPCs to test functions.
type Transport struct {
	// UnaryFunc handles a unary request.
	UnaryFunc func(context.Context, platform.RPCRequest) (platform.RPCResponse, error)
	// ServerStreamFunc handles a server-streaming request.
	ServerStreamFunc func(context.Context, platform.RPCRequest) (platform.ServerStream, error)
}

// Unary implements platform.Transport.
func (t Transport) Unary(ctx context.Context, request platform.RPCRequest) (platform.RPCResponse, error) {
	return t.UnaryFunc(ctx, request)
}

// ServerStream implements platform.Transport.
func (t Transport) ServerStream(ctx context.Context, request platform.RPCRequest) (platform.ServerStream, error) {
	return t.ServerStreamFunc(ctx, request)
}

// ServerStream delegates receive, metadata access and cleanup to test functions.
type ServerStream struct {
	// HeaderFunc supplies initial metadata or an error.
	HeaderFunc func() (platform.Metadata, error)
	// RecvFunc supplies the next message or terminal result.
	RecvFunc func() ([]byte, error)
	// TrailerFunc supplies trailing metadata after completion.
	TrailerFunc func() platform.Metadata
	// CloseFunc cancels blocked operations and releases fake resources.
	CloseFunc func() error
}

// Header implements platform.ServerStream.
func (s ServerStream) Header() (platform.Metadata, error) { return s.HeaderFunc() }

// Recv implements platform.ServerStream.
func (s ServerStream) Recv() ([]byte, error) { return s.RecvFunc() }

// Trailer implements platform.ServerStream.
func (s ServerStream) Trailer() platform.Metadata { return s.TrailerFunc() }

// Close implements platform.ServerStream.
func (s ServerStream) Close() error { return s.CloseFunc() }

var (
	_ platform.Signer       = Signer{}
	_ platform.Transport    = Transport{}
	_ platform.ServerStream = ServerStream{}
)
