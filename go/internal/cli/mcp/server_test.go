package mcp

import (
	"context"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestNew_NotNil(t *testing.T) {
	srv := New(&config.Config{}, nil)
	if srv == nil {
		t.Fatal("New returned nil")
	}
}

func TestGetConn_NilBeforeConnect(t *testing.T) {
	srv := New(&config.Config{}, nil)
	if srv.GetConn() != nil {
		t.Fatal("expected nil connection before connect")
	}
}

func TestGuideResource_ReturnsText(t *testing.T) {
	srv := New(&config.Config{}, nil)
	contents, err := srv.handleGuideResource(context.Background(), mcpgo.ReadResourceRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(contents))
	}
	tc, ok := contents[0].(mcpgo.TextResourceContents)
	if !ok {
		t.Fatalf("expected TextResourceContents, got %T", contents[0])
	}
	if tc.URI != "wendy://guide" {
		t.Errorf("expected URI wendy://guide, got %q", tc.URI)
	}
	if tc.MIMEType != "text/plain" {
		t.Errorf("expected MIME text/plain, got %q", tc.MIMEType)
	}
	if len(tc.Text) < 100 {
		t.Errorf("expected guide text to be at least 100 chars, got %d", len(tc.Text))
	}
	if !strings.Contains(tc.Text, "error_code") {
		t.Errorf("expected guide text to mention error_code, got %q", tc.Text)
	}
}

// TestFileSyncSync_NotRegistered locks in the removal of the dead filesync_sync
// tool: it registers the same tool set Start() does and asserts the tool is
// absent from the server's tool list.
func TestFileSyncSync_NotRegistered(t *testing.T) {
	srv := server.NewMCPServer("t", "0")
	s := New(&config.Config{}, nil)
	s.registerStatusTools(srv)
	s.registerDeviceTools(srv)
	s.registerContainerTools(srv)
	s.registerTelemetryTools(srv)
	s.registerWiFiTools(srv)
	s.registerBluetoothTools(srv)
	s.registerHardwareTools(srv)
	s.registerProvisioningTools(srv)
	s.registerOSTools(srv)
	s.registerCloudTools(srv)
	s.registerContainerMCPTools(context.Background(), srv) // no active connection; no-op

	if _, ok := srv.ListTools()["filesync_sync"]; ok {
		t.Fatal("filesync_sync should not be registered")
	}
}
