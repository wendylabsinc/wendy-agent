package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// fakeUpdateAgentStream satisfies agentpb.WendyAgentService_UpdateAgentClient
// (a grpc.BidiStreamingClient[UpdateAgentRequest, UpdateAgentResponse]) via
// the embedded-nil trick used elsewhere in this package (see
// fakeWriteChunksStream in chunkpush_test.go). It only records the requests
// sent so a test can inspect the final control command.
type fakeUpdateAgentStream struct {
	grpc.BidiStreamingClient[agentpb.UpdateAgentRequest, agentpb.UpdateAgentResponse]
	sent []*agentpb.UpdateAgentRequest
}

func (s *fakeUpdateAgentStream) Send(req *agentpb.UpdateAgentRequest) error {
	s.sent = append(s.sent, req)
	return nil
}

func (s *fakeUpdateAgentStream) CloseSend() error {
	return nil
}

// sendAgentUpdate streams the binary in chunks and then a final control
// command carrying the sha256 and (optionally) a detached signature. This
// pins the seam a signer will populate: whatever bytes the caller passes as
// signature must land verbatim on the v1 proto's Update.Signature field,
// which is what the agent's (currently-disabled) verifier reads.
func TestSendAgentUpdateSignatureReachesProtoField(t *testing.T) {
	stream := &fakeUpdateAgentStream{}
	binaryData := []byte("fake-agent-binary")
	sha256Hash := "deadbeef"
	signature := []byte("fake-ml-dsa65-signature")

	if err := sendAgentUpdate(stream, binaryData, sha256Hash, signature); err != nil {
		t.Fatalf("sendAgentUpdate() error = %v", err)
	}

	if len(stream.sent) == 0 {
		t.Fatal("sendAgentUpdate() sent no requests")
	}
	last := stream.sent[len(stream.sent)-1]
	update := last.GetControl().GetUpdate()
	if update == nil {
		t.Fatalf("last sent request has no Control.Update: %+v", last)
	}
	if update.GetSha256() != sha256Hash {
		t.Errorf("Update.Sha256 = %q, want %q", update.GetSha256(), sha256Hash)
	}
	if !bytes.Equal(update.GetSignature(), signature) {
		t.Errorf("Update.Signature = %q, want %q", update.GetSignature(), signature)
	}
}

// TestSendAgentUpdateNilSignatureLeavesFieldEmpty locks in the no-signer-yet
// default: an absent signature must not synthesize any bytes on the wire.
func TestSendAgentUpdateNilSignatureLeavesFieldEmpty(t *testing.T) {
	stream := &fakeUpdateAgentStream{}
	if err := sendAgentUpdate(stream, []byte("data"), "abc123", nil); err != nil {
		t.Fatalf("sendAgentUpdate() error = %v", err)
	}
	last := stream.sent[len(stream.sent)-1]
	if sig := last.GetControl().GetUpdate().GetSignature(); len(sig) != 0 {
		t.Errorf("Update.Signature = %q, want empty", sig)
	}
}

func TestShouldReapplyBinary(t *testing.T) {
	tests := []struct {
		name           string
		binaryProvided bool
		outcome        osUpdateOutcome
		want           bool
	}{
		{
			name:           "--binary + OS applied + back online → re-apply",
			binaryProvided: true,
			outcome:        osUpdateOutcome{applied: true, online: true},
			want:           true,
		},
		{
			name:           "auto-download path is never re-applied",
			binaryProvided: false,
			outcome:        osUpdateOutcome{applied: true, online: true},
			want:           false,
		},
		{
			name:           "no OS update applied → nothing to survive",
			binaryProvided: true,
			outcome:        osUpdateOutcome{applied: false},
			want:           false,
		},
		{
			name:           "applied but device not confirmed online (cloud) → skip inline re-apply",
			binaryProvided: true,
			outcome:        osUpdateOutcome{applied: true, online: false},
			want:           false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReapplyBinary(tc.binaryProvided, tc.outcome); got != tc.want {
				t.Fatalf("shouldReapplyBinary(%v, %+v) = %v, want %v", tc.binaryProvided, tc.outcome, got, tc.want)
			}
		})
	}
}

func TestWaitForUpdatedAgentReadyRetriesUntilReachable(t *testing.T) {
	started := time.Now()
	attempts := 0

	conn, err := waitForUpdatedAgentReady(context.Background(), func(context.Context) (*grpcclient.AgentConnection, error) {
		attempts++
		if elapsed := time.Since(started); elapsed < 15*time.Millisecond {
			t.Fatalf("reconnect attempted before initial restart delay: %s", elapsed)
		}
		if attempts < 3 {
			return nil, errors.New("agent restarting")
		}
		return &grpcclient.AgentConnection{}, nil
	}, agentRestartWaitOptions{
		InitialDelay: 15 * time.Millisecond,
		Timeout:      200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("waitForUpdatedAgentReady() error = %v", err)
	}
	if conn == nil {
		t.Fatal("waitForUpdatedAgentReady() returned nil connection")
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestWaitForUpdatedAgentReadyReturnsLastReconnectError(t *testing.T) {
	wantErr := errors.New("connection refused")
	attempts := 0

	_, err := waitForUpdatedAgentReady(context.Background(), func(context.Context) (*grpcclient.AgentConnection, error) {
		attempts++
		return nil, wantErr
	}, agentRestartWaitOptions{
		InitialDelay: time.Millisecond,
		Timeout:      20 * time.Millisecond,
		PollInterval: 2 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("waitForUpdatedAgentReady() succeeded, want error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("waitForUpdatedAgentReady() error = %v, want wrapping %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "agent did not become reachable after update") {
		t.Fatalf("waitForUpdatedAgentReady() error = %q, want restart readiness context", err.Error())
	}
	if attempts == 0 {
		t.Fatal("reconnect was never attempted")
	}
}

func TestWaitForUpdatedAgentReadyHonorsCanceledContextDuringInitialDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	attempts := 0
	_, err := waitForUpdatedAgentReady(ctx, func(context.Context) (*grpcclient.AgentConnection, error) {
		attempts++
		return &grpcclient.AgentConnection{}, nil
	}, agentRestartWaitOptions{
		InitialDelay: 50 * time.Millisecond,
		Timeout:      200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForUpdatedAgentReady() error = %v, want context.Canceled", err)
	}
	if attempts != 0 {
		t.Fatalf("reconnect attempts = %d, want 0", attempts)
	}
}

// agentUpdateTerminalError turns the update stream's terminal Recv error into
// what the user is told. A bare io.EOF / dropped transport is NOT a verdict:
// the agent restarts itself the moment the binary lands, which tears down the
// stream before the ack arrives, so those map to errAgentUpdateUnconfirmed for
// the caller to verify. Real gRPC statuses are surfaced with their message.
func TestAgentUpdateTerminalError(t *testing.T) {
	tests := []struct {
		name            string
		recvErr         error
		wantUnconfirmed bool
		wantSubstr      string
	}{
		{
			name:            "bare EOF is unconfirmed, not a failure",
			recvErr:         io.EOF,
			wantUnconfirmed: true,
		},
		{
			name:            "transport closing is unconfirmed",
			recvErr:         status.Error(codes.Unavailable, "transport is closing"),
			wantUnconfirmed: true,
		},
		{
			name:            "client cancel is unconfirmed",
			recvErr:         status.Error(codes.Canceled, "context canceled"),
			wantUnconfirmed: true,
		},
		{
			name:       "update already in progress explains the stale-lock reboot",
			recvErr:    status.Error(codes.FailedPrecondition, "an update is already in progress"),
			wantSubstr: "reboot",
		},
		{
			name:       "sha mismatch is reported verbatim",
			recvErr:    status.Error(codes.DataLoss, "SHA256 mismatch: expected aa, got bb"),
			wantSubstr: "SHA256 mismatch",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := agentUpdateTerminalError(tc.recvErr)
			if err == nil {
				t.Fatal("agentUpdateTerminalError = nil, want error")
			}
			if got := errors.Is(err, errAgentUpdateUnconfirmed); got != tc.wantUnconfirmed {
				t.Fatalf("errors.Is(err, errAgentUpdateUnconfirmed) = %v, want %v (err: %v)", got, tc.wantUnconfirmed, err)
			}
			if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q should contain %q", err, tc.wantSubstr)
			}
		})
	}
}

// After an unconfirmed upload the CLI reconnects and checks what the device
// actually runs: the expected release version (or newer) proves the update
// landed; anything else means the old agent is still in place.
func TestAgentUpdateVerified(t *testing.T) {
	tests := []struct {
		name     string
		reported string
		expected string
		want     bool
	}{
		{"exact match", "2026.07.01-223311", "2026.07.01-223311", true},
		{"newer than expected", "2026.07.02-000001", "2026.07.01-223311", true},
		{"older agent still running", "2026.06.30-120000", "2026.07.01-223311", false},
		{"no expectation (--binary) passes", "dev-abc123", "", true},
		{"unknown reported version fails", "", "2026.07.01-223311", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentUpdateVerified(tc.reported, tc.expected); got != tc.want {
				t.Fatalf("agentUpdateVerified(%q, %q) = %v, want %v", tc.reported, tc.expected, got, tc.want)
			}
		})
	}
}
