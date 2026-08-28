package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// unavailableContainerClient always fails QueryChunks with a gRPC Unavailable
// status, modelling a tunnel that's already down by the time the chunk
// push's capability probe goes out. calls counts invocations so tests can
// assert the authoritative capability probe runs exactly once per attempt.
// The optional whole-layer query may start alongside it, but no layer is ever
// materialized or uploaded after the capability probe fails.
type unavailableContainerClient struct {
	agentpb.WendyContainerServiceClient // embedded nil
	calls                               int
}

func (c *unavailableContainerClient) QueryChunks(_ context.Context, _ *agentpb.QueryChunksRequest, _ ...grpc.CallOption) (*agentpb.QueryChunksResponse, error) {
	c.calls++
	return nil, status.Error(codes.Unavailable, "tunnel dropped")
}

func (c *unavailableContainerClient) QueryLayers(_ context.Context, _ *agentpb.QueryLayersRequest, _ ...grpc.CallOption) (*agentpb.QueryLayersResponse, error) {
	return nil, status.Error(codes.Unavailable, "tunnel dropped")
}

// closeTracker is an io.Closer that records whether it was closed, via
// AgentConnection.ExtraClosers, so tests can assert exactly which
// connections pushLayersResumingTunnelDrops closed as "intermediate"
// (reconnected away from, but neither the original conn passed in nor the
// one ultimately returned).
type closeTracker struct{ closed *bool }

func (c closeTracker) Close() error {
	*c.closed = true
	return nil
}

// TestRetryableTunnelError is a table test over retryableTunnelError's
// classification: gRPC Unavailable (bare or wrapped) and an EOF-shaped
// stream death are retryable; Unimplemented, context cancellation, and an
// imageBuildFailedError are not.
func TestRetryableTunnelError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"Unavailable bare", status.Error(codes.Unavailable, "tunnel dropped"), true},
		{"Unavailable wrapped", fmt.Errorf("writechunks: %w", status.Error(codes.Unavailable, "tunnel dropped")), true},
		{"Unimplemented", status.Error(codes.Unimplemented, "no chunk-diff support"), false},
		{"Unimplemented wrapped", fmt.Errorf("querychunks: %w", status.Error(codes.Unimplemented, "no chunk-diff support")), false},
		{"context.Canceled", context.Canceled, false},
		{"context.Canceled wrapped", fmt.Errorf("push: %w", context.Canceled), false},
		{"imageBuildFailedError", &imageBuildFailedError{errors.New("build failed")}, false},
		{"wrapped io.EOF", fmt.Errorf("recv: %w", io.EOF), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableTunnelError(tc.err); got != tc.want {
				t.Errorf("retryableTunnelError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestPushLayersResumingTunnelDropsReconnectsAndRetries drives the resume
// loop through exactly one drop: connA's capability probe reports
// Unavailable, connA.Reconnect hands back connB (a fakeContainerClient from
// chunkpush_test.go, configured so the single layer resolves as
// already-present on the device — no chunking needed), and attempt 2 on
// connB succeeds outright. Asserts the reconnected connection (connB) is
// what's returned, exactly one reconnect happened, and the resulting header
// carries the device-reported diff ID/size.
func TestPushLayersResumingTunnelDropsReconnectsAndRetries(t *testing.T) {
	manifestCacheTestDir = t.TempDir()
	t.Cleanup(func() { manifestCacheTestDir = "" })

	restore := forceBuildProgressInteractive(false)
	defer restore()
	var out strings.Builder
	restoreOut := setBuildProgressOut(&out)
	defer restoreOut()

	diffID := "sha256:" + strings.Repeat("ab", 32)
	const presentSize int64 = 4096
	layers := []localLayer{{
		Digest:    "sha256:" + sha256Hex([]byte("compressed-bytes")),
		DiffID:    diffID,
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		// Intentionally not valid gzip: if the push tried to decompress this
		// layer (rather than resolving it as already-present) it would fail.
		Blob: []byte("this is not gzip"),
	}}

	unavailable := &unavailableContainerClient{}
	connA := &grpcclient.AgentConnection{ContainerService: unavailable}

	fakeB := &fakeContainerClient{
		queryFn: func(_ *agentpb.QueryChunksRequest) *agentpb.QueryChunksResponse {
			return &agentpb.QueryChunksResponse{}
		},
		queryLayersFn: func(_ *agentpb.QueryLayersRequest) *agentpb.QueryLayersResponse {
			return &agentpb.QueryLayersResponse{
				Present: []*agentpb.PresentLayer{{DiffId: diffID, Size: presentSize}},
			}
		},
	}
	connB := &grpcclient.AgentConnection{ContainerService: fakeB}

	reconnects := 0
	connA.Reconnect = func(context.Context) (*grpcclient.AgentConnection, error) {
		reconnects++
		return connB, nil
	}
	connB.Reconnect = func(context.Context) (*grpcclient.AgentConnection, error) {
		t.Fatal("connB.Reconnect should not be called; attempt 2 (on connB) must succeed")
		return nil, nil
	}

	gotConn, headers, err := pushLayersResumingTunnelDrops(context.Background(), connA, layers, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotConn != connB {
		t.Fatalf("returned conn = %p, want connB (%p)", gotConn, connB)
	}
	if reconnects != 1 {
		t.Fatalf("reconnects = %d, want 1", reconnects)
	}
	if unavailable.calls != 1 {
		t.Fatalf("connA probe calls = %d, want 1 (one failed attempt, then reconnect)", unavailable.calls)
	}
	if len(headers) != 1 {
		t.Fatalf("expected 1 header, got %d", len(headers))
	}
	if h := headers[0]; h.GetDiffId() != diffID || h.GetSize() != presentSize {
		t.Fatalf("header = {diffID:%q size:%d}, want {diffID:%q size:%d}", h.GetDiffId(), h.GetSize(), diffID, presentSize)
	}
}

// TestPushLayersResumingTunnelDropsDoesNotRetryUnimplemented verifies that an
// Unimplemented probe error (an agent with no chunk-diff support at all)
// bubbles straight up without ever calling Reconnect, so deployByChunkDiff's
// caller can fall back to the registry-push ladder instead of retrying a
// push that can never succeed.
func TestPushLayersResumingTunnelDropsDoesNotRetryUnimplemented(t *testing.T) {
	restore := forceBuildProgressInteractive(false)
	defer restore()
	var out strings.Builder
	restoreOut := setBuildProgressOut(&out)
	defer restoreOut()

	conn := &grpcclient.AgentConnection{ContainerService: &probeUnsupportedClient{}}
	conn.Reconnect = func(context.Context) (*grpcclient.AgentConnection, error) {
		t.Fatal("Reconnect must not be called for an Unimplemented probe error")
		return nil, nil
	}

	gotConn, headers, err := pushLayersResumingTunnelDrops(context.Background(), conn, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !isUnimplementedRPCError(err) {
		t.Fatalf("expected an Unimplemented error, got %v", err)
	}
	if headers != nil {
		t.Fatalf("expected nil headers on failure, got %v", headers)
	}
	if gotConn != conn {
		t.Fatalf("expected the original conn returned unchanged, got %p want %p", gotConn, conn)
	}
}

// TestPushLayersResumingTunnelDropsGivesUpAfterAttempts drives three straight
// Unavailable failures (connA -> connB -> connC, one probe failure each) and
// asserts the loop stops at chunkPushResumeAttempts: exactly
// chunkPushResumeAttempts-1 reconnects happen, connC.Reconnect is never
// reached, the final attempt's connection (connC) is what's returned even on
// failure (so the caller can still close it), and connB — reconnected away
// from mid-loop, neither the original conn nor the one finally returned — is
// closed by the loop itself.
func TestPushLayersResumingTunnelDropsGivesUpAfterAttempts(t *testing.T) {
	restore := forceBuildProgressInteractive(false)
	defer restore()
	var out strings.Builder
	restoreOut := setBuildProgressOut(&out)
	defer restoreOut()

	connA := &grpcclient.AgentConnection{ContainerService: &unavailableContainerClient{}}
	connB := &grpcclient.AgentConnection{ContainerService: &unavailableContainerClient{}}
	connC := &grpcclient.AgentConnection{ContainerService: &unavailableContainerClient{}}

	var bClosed, cClosed bool
	connB.ExtraClosers = append(connB.ExtraClosers, closeTracker{&bClosed})
	connC.ExtraClosers = append(connC.ExtraClosers, closeTracker{&cClosed})

	reconnects := 0
	connA.Reconnect = func(context.Context) (*grpcclient.AgentConnection, error) {
		reconnects++
		return connB, nil
	}
	connB.Reconnect = func(context.Context) (*grpcclient.AgentConnection, error) {
		reconnects++
		return connC, nil
	}
	connC.Reconnect = func(context.Context) (*grpcclient.AgentConnection, error) {
		t.Fatal("Reconnect called beyond the attempts cap")
		return nil, nil
	}

	gotConn, headers, err := pushLayersResumingTunnelDrops(context.Background(), connA, nil, nil)
	if err == nil {
		t.Fatal("expected an error after exhausting all attempts")
	}
	if headers != nil {
		t.Fatalf("expected nil headers on failure, got %v", headers)
	}
	if gotConn != connC {
		t.Fatalf("returned conn = %p, want connC (%p) — the final attempt's connection", gotConn, connC)
	}
	if reconnects != chunkPushResumeAttempts-1 {
		t.Fatalf("reconnects = %d, want %d (chunkPushResumeAttempts-1)", reconnects, chunkPushResumeAttempts-1)
	}
	if !bClosed {
		t.Error("expected connB (an intermediate conn) to be closed by the resume loop")
	}
	if cClosed {
		t.Error("the final conn is the caller's to close, not pushLayersResumingTunnelDrops's")
	}
}
