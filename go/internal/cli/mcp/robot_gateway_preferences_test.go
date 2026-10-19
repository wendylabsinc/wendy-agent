package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

func TestCloudLinkedPreferencesAreTransientAndAccountScoped(t *testing.T) {
	cfg := linkedGatewayConfig(t)
	cfg.HTTP.CloudLink.Scopes = append(cfg.HTTP.CloudLink.Scopes, RobotSettingsScope)
	// A prior version's file is neither imported nor overwritten by hosted
	// preferences. Persisted credentials in this directory remain independent.
	hash := sha256.Sum256([]byte("alice"))
	oldPath := filepath.Join(cfg.StateDirectory, fmt.Sprintf("settings-%x.json", hash[:16]))
	oldData := []byte(`{"show_3d":false,"include_offline":true}`)
	if err := os.WriteFile(oldPath, oldData, 0600); err != nil {
		t.Fatal(err)
	}
	newGateway := func() *RobotGateway {
		t.Helper()
		g, err := NewRobotGateway(cfg, func(context.Context, string) (*grpcclient.AgentConnection, error) {
			t.Fatal("preferences must not access a device")
			return nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = g.httpHandler(&http.Client{}, func(string) string {
			return base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
		})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	principalContext := func(subject string) context.Context {
		return context.WithValue(context.Background(), gatewayPrincipalKey{}, gatewayPrincipal{
			Subject: subject, Scopes: []string{RobotReadScope, RobotSettingsScope},
		})
	}
	read := func(g *RobotGateway, subject string) map[string]any {
		t.Helper()
		result, err := g.protocol.ListTools()["read_device_settings"].Handler(principalContext(subject), callToolReq("read_device_settings", nil))
		if err != nil || result.IsError {
			t.Fatalf("read preferences: %v %v", result, err)
		}
		return result.StructuredContent.(gatewaySettingsResult).Values
	}
	g := newGateway()
	if got := read(g, "alice"); got["show_3d"] != true || got["include_offline"] != false {
		t.Fatalf("hosted preferences loaded old file: %v", got)
	}
	result, err := g.protocol.ListTools()["update_device_settings"].Handler(principalContext("alice"), callToolReq("update_device_settings", map[string]any{"set": map[string]any{"show_3d": false, "include_offline": true}}))
	if err != nil || result.IsError {
		t.Fatalf("update preferences: %v %v", result, err)
	}
	if got := read(g, "alice"); got["show_3d"] != false || got["include_offline"] != true {
		t.Fatalf("preferences did not survive within the process: %v", got)
	}
	if got := read(g, "bob"); got["show_3d"] != true || got["include_offline"] != false {
		t.Fatalf("preferences leaked to another account: %v", got)
	}
	// A tool caller must not receive a shared mutable preferences map.
	got := read(g, "alice")
	got["show_3d"] = true
	if read(g, "alice")["show_3d"] != false {
		t.Fatal("returned preferences map mutated stored values")
	}
	g.CloseCloudLink()
	if len(g.workspacePreferences) != 0 {
		t.Fatal("closed gateway retained transient preferences")
	}
	restarted := newGateway()
	defer restarted.CloseCloudLink()
	if got := read(restarted, "alice"); got["show_3d"] != true || got["include_offline"] != false {
		t.Fatalf("hosted preferences survived restart: %v", got)
	}
	raw, err := os.ReadFile(oldPath)
	if err != nil || string(raw) != string(oldData) {
		t.Fatal("hosted preferences wrote the prior file", err)
	}
	files, err := filepath.Glob(filepath.Join(cfg.StateDirectory, "settings-*.json"))
	if err != nil || len(files) != 1 || files[0] != oldPath {
		t.Fatal("hosted preferences created settings files", files, err)
	}
}

func TestLocalGatewayPreferencesStillPersist(t *testing.T) {
	cfg := gatewayTestConfig()
	cfg.StateDirectory = t.TempDir()
	newGateway := func() *RobotGateway {
		t.Helper()
		g, err := NewRobotGateway(cfg, func(context.Context, string) (*grpcclient.AgentConnection, error) { return nil, fmt.Errorf("unused") })
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	ctx := context.WithValue(context.Background(), gatewayPrincipalKey{}, gatewayPrincipal{Subject: "alice", Scopes: robotGatewayScopes})
	g := newGateway()
	result, err := g.protocol.ListTools()["update_device_settings"].Handler(ctx, callToolReq("update_device_settings", map[string]any{"set": map[string]any{"show_3d": false}}))
	if err != nil || result.IsError {
		t.Fatalf("update local preferences: %v %v", result, err)
	}
	result, err = newGateway().protocol.ListTools()["read_device_settings"].Handler(ctx, callToolReq("read_device_settings", nil))
	if err != nil || result.IsError || result.StructuredContent.(gatewaySettingsResult).Values["show_3d"] != false {
		t.Fatalf("local preferences did not persist: %v %v", result, err)
	}
}
