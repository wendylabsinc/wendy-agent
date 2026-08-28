package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

// A cloud-tunnel connection carries a Reconnect closure pinned to the exact
// asset id (cloud_tunnel.go). updatedAgentReconnectFunc must reuse it rather
// than re-running device discovery: discovery relaunches the interactive cloud
// picker mid-"Waiting for agent to restart..." when the device was picked
// interactively (no --device, so cloudDeviceConfig.DeviceName is empty), and
// even with a name it can misresolve while the restarting device's heartbeat
// lapses.
func TestUpdatedAgentReconnectFuncPrefersPinnedReconnect(t *testing.T) {
	sentinel := errors.New("pinned reconnect used")
	previous := &grpcclient.AgentConnection{
		Reconnect: func(context.Context) (*grpcclient.AgentConnection, error) {
			return nil, sentinel
		},
	}
	// Simulate `wendy cloud device update` with an interactively picked device:
	// cloud config present, DeviceName empty.
	ctx := context.WithValue(context.Background(), cloudDeviceContextKey{}, cloudDeviceConfig{})

	reconnect := updatedAgentReconnectFunc(ctx, previous)

	// Already-cancelled so that, should the closure wrongly re-run discovery,
	// it fails fast instead of touching auth config or the network.
	rctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := reconnect(rctx)
	if !errors.Is(err, sentinel) {
		t.Fatalf("reconnect did not use the connection's pinned Reconnect; got err=%v", err)
	}
}
