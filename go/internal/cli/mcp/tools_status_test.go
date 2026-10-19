package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestWendyStatus_NotConnected(t *testing.T) {
	srv := New(&config.Config{}, nil)
	result, err := srv.handleWendyStatus(context.Background(), callToolReq("wendy_status", nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", result.Content)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &out); err != nil {
		t.Fatalf("parsing result: %v", err)
	}
	if out["connected"] != false {
		t.Errorf("expected connected=false, got %v", out["connected"])
	}
	if out["suggested_next_step"] == "" {
		t.Error("expected non-empty suggested_next_step")
	}
}

func notConnectedNextStep(t *testing.T, srv *mcpServer) string {
	t.Helper()
	result, err := srv.handleWendyStatus(context.Background(), callToolReq("wendy_status", nil))
	if err != nil || result.IsError {
		t.Fatalf("wendy_status: %v %v", result, err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &out); err != nil {
		t.Fatalf("parsing result: %v", err)
	}
	next, _ := out["suggested_next_step"].(string)
	return next
}

// Without hardware, the next step must not dead-end at "connect a device": a
// local simulator covers most on-device checks. Booting an existing VM needs no
// approval; creating one downloads an image, so the user decides.
func TestWendyStatus_NotConnectedOffersSimulators(t *testing.T) {
	srv := New(&config.Config{}, nil)
	srv.SetSimulatorBackend(SimulatorBackend{List: func(context.Context) ([]SimulatorInfo, error) { return nil, nil }})
	next := notConnectedNextStep(t, srv)
	for _, want := range []string{"vm:NAME", "simulator_create", "ask the user", "os_install_plan"} {
		if !strings.Contains(next, want) {
			t.Errorf("suggested_next_step does not mention %q: %s", want, next)
		}
	}
}

func TestWendyStatus_NotConnectedOmitsUnavailableSimulators(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*mcpServer)
	}{
		{"no backend", func(*mcpServer) {}},
		{"relay", func(s *mcpServer) {
			s.SetSimulatorBackend(SimulatorBackend{List: func(context.Context) ([]SimulatorInfo, error) { return nil, nil }})
			s.relay = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := New(&config.Config{}, nil)
			tc.setup(srv)
			if next := notConnectedNextStep(t, srv); strings.Contains(next, "simulator") {
				t.Errorf("suggested_next_step offers simulators this server cannot manage: %s", next)
			}
		})
	}
}

func TestWendyStatus_Connected_Direct(t *testing.T) {
	conn, _ := startFakeAgentServer(t, &fakeAgentServer{})
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)
	srv.SetConnType("direct")

	result, err := srv.handleWendyStatus(context.Background(), callToolReq("wendy_status", nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &out); err != nil {
		t.Fatalf("parsing result: %v", err)
	}
	if out["connected"] != true {
		t.Errorf("expected connected=true, got %v", out["connected"])
	}
	if out["connection_type"] != "direct" {
		t.Errorf("expected connection_type=direct, got %v", out["connection_type"])
	}
}
