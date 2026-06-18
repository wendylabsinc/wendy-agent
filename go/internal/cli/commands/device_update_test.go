package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

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
