//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"go.uber.org/zap"
)

func TestCarrierStartupFailureAndConfigurationRemoval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local-mesh.json")
	if err := os.WriteFile(path, []byte(`{"nan":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nan-mesh.json"), []byte(`{"participate":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	sharing := &meshSharingManager{}
	// Missing enrollment cert fails before any kernel/network mutation.
	for range 2 {
		err := runConfiguredNetCarriers(context.Background(), dir, localmesh.TCPIdentity{}, zap.NewNop(), nil, nil, sharing, nil)
		if err == nil {
			t.Fatal("invalid identity was accepted")
		}
		state := sharing.Status()
		if state.State != "failed" || !strings.Contains(state.Detail, "NAN: failed") {
			t.Fatalf("startup failure masked: %+v", state)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := runConfiguredNetCarriers(context.Background(), dir, localmesh.TCPIdentity{}, zap.NewNop(), nil, nil, sharing, nil); err != nil {
		t.Fatal(err)
	}
	if state := sharing.Status(); state.Available || state.State != "" {
		t.Fatalf("removed config retained failure: %+v", state)
	}
}

func TestParticipationOverridesEnabledCarriersBeforeIdentityOrNetwork(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"nan":true,"ble":true,"ethernet":true,"infrastructureWifi":true,"listen":"127.0.0.1:43023"}`)
	path := filepath.Join(dir, "local-mesh.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	sharing := &meshSharingManager{}
	// No policy file means participation off, even with explicit carriers and
	// invalid credentials. There must be no attempt at identity/network setup.
	if err := runConfiguredNetCarriers(context.Background(), dir, localmesh.TCPIdentity{}, zap.NewNop(), nil, nil, sharing, nil); err != nil {
		t.Fatal(err)
	}
	if state := sharing.Status(); state.State != "disabled" || state.AuthenticatedPeers != 0 {
		t.Fatalf("disabled state: %+v", state)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(raw) {
		t.Fatalf("saved choices changed: %s %v", saved, err)
	}
}

func TestCarrierStartupFailureWithoutEnabledCarrierNames(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"unrecognizedCarrier":true}`} {
		t.Run(raw, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "local-mesh.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			sharing := &meshSharingManager{}
			// Exercise the status initialization before invalid credentials stop
			// startup, without touching host routing or network interfaces.
			err := runEnabledMeshCarriers(context.Background(), dir, localmesh.TCPIdentity{}, zap.NewNop(), nil, nil, sharing, nil)
			if err == nil {
				t.Fatal("invalid startup accepted")
			}
			state := sharing.Status()
			if !state.Available || state.State != "failed" || !strings.Contains(state.Detail, "Local net: failed") {
				t.Fatalf("startup failure hidden without named carriers: %+v", state)
			}
		})
	}
}
